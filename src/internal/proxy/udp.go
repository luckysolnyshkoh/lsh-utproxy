package proxy

import (
	"context"
	"errors"
	"lsh-utproxy/internal/acl"
	"lsh-utproxy/internal/config"
	"lsh-utproxy/internal/stats"
	"lsh-utproxy/internal/util"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

var ErrTooManySessions = errors.New("udp session limit reached")

// udpLimitOnce keeps the "limit reached" warning to one line instead of one per
// dropped packet.
var udpLimitOnce sync.Once

type udpSession struct {
	src      *net.UDPAddr
	beConn   *net.UDPConn
	backend  string
	serverID string
	ttl      time.Duration
	created  time.Time
	lastSeen atomic.Int64 // unix nanos: written by readLoop and relayBack
	closed   atomic.Bool
	bufSz    int
}

func (s *udpSession) touch() { s.lastSeen.Store(time.Now().UnixNano()) }

func (s *udpSession) sinceSeen() time.Duration {
	return time.Since(time.Unix(0, s.lastSeen.Load()))
}

type udpProxy struct {
	fe         config.Frontend
	cfg        *config.Config
	acl        *acl.List
	lconn      *net.UDPConn
	backendMap map[string]*BackendSet
	st         *stats.Stats
	sessions   sync.Map
	ctx        context.Context
	ttl        time.Duration
	maxSess    int
	bufSz      int
}

// listenUDP binds the frontend UDP socket.
func listenUDP(cfg *config.Config, addr string) (*net.UDPConn, error) {
	if cfg.Defaults.ReUsePort && util.ReusePortSupported {
		return util.ListenUDPReuse(addr)
	}
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	return net.ListenUDP("udp", ua)
}

// serveUDP runs the UDP session machinery on an already bound socket until ctx
// is done. aclList is the frontend's ordered allow/deny/drop rules; nil or an
// empty list means no filtering. Both deny and drop discard the datagram: a
// userspace UDP proxy cannot send the ICMP unreachable that REJECT would need.
func serveUDP(ctx context.Context, cfg *config.Config, fe config.Frontend, aclList *acl.List, lconn *net.UDPConn, backendMap map[string]*BackendSet, st *stats.Stats) error {
	u := &udpProxy{
		fe:         fe,
		cfg:        cfg,
		acl:        aclList,
		lconn:      lconn,
		backendMap: backendMap,
		st:         st,
		ctx:        ctx,
		ttl:        cfg.Defaults.UDPSessionTTL,
		maxSess:    cfg.Defaults.UDPMaxSessions,
		bufSz:      cfg.Defaults.UDPBufferSize,
	}
	if u.ttl <= 0 {
		u.ttl = 30 * time.Second
	}
	if u.maxSess <= 0 {
		u.maxSess = 10000
	}
	if u.bufSz <= 0 {
		u.bufSz = 64 * 1024
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		u.gcLoop()
	}()
	go func() {
		defer wg.Done()
		u.readLoop()
	}()

	<-ctx.Done()
	// Closing the socket unblocks the pending ReadFromUDP.
	_ = lconn.Close()
	wg.Wait()
	return ctx.Err()
}

func (u *udpProxy) sessionKey(src *net.UDPAddr) string {
	return u.fe.Name + "|" + src.String()
}

// udpLimits resolves the per-backend UDP knobs, falling back to the frontend
// and the defaults. The backend values come from the listen/backend section, so
// "udp-session-ttl 40s" inside a listen block is actually applied.
func (u *udpProxy) limits(bset *BackendSet) (ttl time.Duration, maxSess, bufSz int) {
	ttl, maxSess, bufSz = u.ttl, u.maxSess, u.bufSz
	if bset == nil || bset.bcfg == nil {
		return
	}
	b := bset.bcfg
	if b.UDPSessionTTL > 0 {
		ttl = b.UDPSessionTTL
	} else if b.TimeoutIdle > 0 {
		ttl = b.TimeoutIdle
	} else if u.fe.TimeoutIdle > 0 {
		ttl = u.fe.TimeoutIdle
	}
	if b.UDPMaxSessions > 0 {
		maxSess = b.UDPMaxSessions
	}
	if b.UDPBufferSize > 0 {
		bufSz = b.UDPBufferSize
	}
	return
}

func (u *udpProxy) getOrCreateSession(src *net.UDPAddr) (*udpSession, error) {
	k := u.sessionKey(src)
	if v, ok := u.sessions.Load(k); ok {
		s := v.(*udpSession)
		s.touch()
		return s, nil
	}
	bn := u.fe.DefaultBackend
	bset, ok := u.backendMap[bn]
	if !ok || bset == nil {
		return nil, errors.New("no backend")
	}
	ttl, maxSess, bufSz := u.limits(bset)
	if maxSess > 0 && int(u.st.UDPSessions.Load()) >= maxSess {
		return nil, ErrTooManySessions
	}
	bsrv, berr := bset.Next(src.IP.String())
	if berr != nil {
		return nil, berr
	}
	bsrvS, ok2 := bsrv.(*BackendServer)
	if !ok2 {
		return nil, errors.New("bad server")
	}
	if !bsrvS.TryInc(bsrvS.MaxConn()) {
		return nil, ErrServerFull
	}
	rb, err := net.ResolveUDPAddr("udp", bsrvS.Addr())
	if err != nil {
		bsrvS.Dec()
		return nil, err
	}
	beConn, err := net.DialUDP("udp", nil, rb)
	if err != nil {
		bsrvS.Dec()
		return nil, err
	}
	s := &udpSession{
		src:      src,
		beConn:   beConn,
		backend:  bn,
		serverID: bsrvS.ID(),
		ttl:      ttl,
		created:  time.Now(),
		bufSz:    bufSz,
	}
	s.touch()
	// Two packets from the same client may arrive at the same instant: only one
	// session may win, otherwise the loser leaks a socket and its relay goroutine
	// deletes the live session from the map when it stops.
	if actual, loaded := u.sessions.LoadOrStore(k, s); loaded {
		_ = beConn.Close()
		bsrvS.Dec()
		w := actual.(*udpSession)
		w.touch()
		return w, nil
	}
	u.st.UDPSessions.Add(1)
	go u.relayBack(s, bsrvS)
	return s, nil
}

func (u *udpProxy) relayBack(s *udpSession, bsrvS *BackendServer) {
	buf := make([]byte, s.bufSz)
	defer func() {
		_ = s.beConn.Close()
		bsrvS.Dec()
		if s.closed.CompareAndSwap(false, true) {
			u.sessions.CompareAndDelete(u.sessionKey(s.src), s)
			u.st.UDPSessions.Add(-1)
		}
	}()
	for {
		select {
		case <-u.ctx.Done():
			return
		default:
		}
		_ = s.beConn.SetReadDeadline(time.Now().Add(s.ttl))
		n, err := s.beConn.Read(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				if s.sinceSeen() > s.ttl {
					return
				}
				continue
			}
			return
		}
		if n == 0 {
			continue
		}
		s.touch()
		_ = u.lconn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		wn, werr := u.lconn.WriteToUDP(buf[:n], s.src)
		if werr != nil {
			continue
		}
		u.st.PacketsOut.Add(1)
		u.st.BytesOut.Add(int64(wn))
	}
}

func (u *udpProxy) readLoop() {
	buf := make([]byte, u.bufSz)
	for {
		select {
		case <-u.ctx.Done():
			return
		default:
		}
		_ = u.lconn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, src, err := u.lconn.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		if n == 0 {
			continue
		}
		u.st.PacketsIn.Add(1)
		u.st.BytesIn.Add(int64(n))
		if u.acl.Len() > 0 {
			act, rule := u.acl.Decide(src.IP)
			if act != acl.ActionAllow {
				u.st.Blocked.Add(1)
				if u.acl.ShouldLog() {
					log.Info().Str("frontend", u.fe.Name).Str("rule", rule).Str("action", act.String()).
						Str("src", src.String()).
						Msg("udp datagram refused by access rule")
				}
				continue
			}
		}
		sess, gerr := u.getOrCreateSession(src)
		if gerr != nil {
			u.st.Errors.Add(1)
			if errors.Is(gerr, ErrTooManySessions) {
				u.st.Dropped.Add(1)
				udpLimitOnce.Do(func() {
					log.Warn().Str("frontend", u.fe.Name).Int("udp_max_sessions", u.maxSess).
						Msg("udp session limit reached, packets are dropped until sessions expire")
				})
			} else if errors.Is(gerr, ErrServerFull) {
				u.st.Dropped.Add(1)
			}
			continue
		}
		_ = sess.beConn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		wn, werr := sess.beConn.Write(buf[:n])
		if werr != nil {
			u.st.Errors.Add(1)
			continue
		}
		sess.touch()
		u.st.PacketsOut.Add(1)
		u.st.BytesOut.Add(int64(wn))
	}
}

func (u *udpProxy) gcLoop() {
	period := u.ttl / 2
	if period < time.Second {
		period = time.Second
	}
	if period > 5*time.Second {
		period = 5 * time.Second
	}
	t := time.NewTicker(period)
	defer t.Stop()
	for {
		select {
		case <-u.ctx.Done():
			return
		case <-t.C:
			u.sessions.Range(func(_, v any) bool {
				s := v.(*udpSession)
				if s.sinceSeen() > s.ttl {
					// Closing the socket makes relayBack return and remove the
					// session from the map.
					_ = s.beConn.Close()
				}
				return true
			})
		}
	}
}
