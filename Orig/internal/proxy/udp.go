package proxy

import (
	"context"
	"errors"
	"lsh-utproxy/internal/config"
	"lsh-utproxy/internal/stats"
	"lsh-utproxy/internal/util"
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

type udpSession struct {
	src      *net.UDPAddr
	beConn   *net.UDPConn
	backend  string
	serverID string
	lastSeen time.Time
	created  time.Time
	bytesIn  int64
	bytesOut int64
}

type udpProxy struct {
	fe         config.Frontend
	cfg        *config.Config
	lconn      *net.UDPConn
	backendMap map[string]*BackendSet
	st         *stats.Stats
	sessions   sync.Map
	ctx        context.Context
	ttl        time.Duration
	maxSess    int
	bufSz      int
	wg         sync.WaitGroup
}

func startUDP(ctx context.Context, cfg *config.Config, fe config.Frontend, addr string, backendMap map[string]*BackendSet, st *stats.Stats) error {
	u := &udpProxy{
		fe:         fe,
		cfg:        cfg,
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
	var err error
	reuse := cfg.Defaults.ReUsePort
	if reuse {
		u.lconn, err = util.ListenUDPReuse(addr)
	} else {
		ua, e := net.ResolveUDPAddr("udp", addr)
		if e != nil {
			return e
		}
		u.lconn, e = net.ListenUDP("udp", ua)
		if e != nil {
			return e
		}
	}
	if err != nil {
		return err
	}
	defer u.lconn.Close()
	log.Info().Str("frontend", fe.Name).Str("mode", "udp").Str("addr", addr).Msg("listening")
	u.wg.Add(2)
	go u.gcLoop()
	go u.readLoop()
	u.wg.Wait()
	return nil
}

func (u *udpProxy) sessionKey(src *net.UDPAddr) string {
	return u.fe.Name + "|" + src.String()
}

func (u *udpProxy) getOrCreateSession(src *net.UDPAddr) (*udpSession, error) {
	k := u.sessionKey(src)
	if v, ok := u.sessions.Load(k); ok {
		s := v.(*udpSession)
		s.lastSeen = time.Now()
		return s, nil
	}
	bn := u.fe.DefaultBackend
	bset, ok := u.backendMap[bn]
	if !ok || bset == nil {
		return nil, errors.New("no backend")
	}
	bsrv, berr := bset.Next()
	if berr != nil {
		return nil, berr
	}
	bsrvS, ok2 := bsrv.(*BackendServer)
	if !ok2 {
		return nil, errors.New("bad server")
	}
	if bsrvS.MaxConn() > 0 && bsrvS.Active() >= int64(bsrvS.MaxConn()) {
		return nil, ErrServerFull
	}
	rb, err := net.ResolveUDPAddr("udp", bsrvS.Addr())
	if err != nil {
		return nil, err
	}
	beConn, err := net.DialUDP("udp", nil, rb)
	if err != nil {
		return nil, err
	}
	bsrvS.Inc()
	s := &udpSession{
		src:      src,
		beConn:   beConn,
		backend:  bn,
		serverID: bsrvS.ID(),
		lastSeen: time.Now(),
		created:  time.Now(),
	}
	u.sessions.Store(k, s)
	u.st.UDPSessions.Add(1)
	go u.relayBack(s, bsrvS)
	return s, nil
}

func (u *udpProxy) relayBack(s *udpSession, bsrvS *BackendServer) {
	buf := make([]byte, u.bufSz)
	defer func() {
		_ = s.beConn.Close()
		bsrvS.Dec()
		u.sessions.Delete(u.sessionKey(s.src))
		u.st.UDPSessions.Add(-1)
	}()
	for {
		select {
		case <-u.ctx.Done():
			return
		default:
		}
		_ = s.beConn.SetReadDeadline(time.Now().Add(u.ttl))
		n, err := s.beConn.Read(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if time.Since(s.lastSeen) > u.ttl {
					return
				}
				continue
			}
			return
		}
		if n == 0 {
			continue
		}
		s.lastSeen = time.Now()
		_ = u.lconn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		wn, werr := u.lconn.WriteToUDP(buf[:n], s.src)
		if werr != nil {
			continue
		}
		s.bytesOut += int64(wn)
		u.st.PacketsOut.Add(1)
		u.st.BytesOut.Add(int64(wn))
	}
}

func (u *udpProxy) readLoop() {
	defer u.wg.Done()
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
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
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
		sess, gerr := u.getOrCreateSession(src)
		if gerr != nil {
			u.st.Errors.Add(1)
			continue
		}
		_ = sess.beConn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		wn, werr := sess.beConn.Write(buf[:n])
		if werr != nil {
			u.st.Errors.Add(1)
			continue
		}
		sess.bytesIn += int64(wn)
		sess.lastSeen = time.Now()
		u.st.PacketsOut.Add(1)
		u.st.BytesOut.Add(int64(wn))
	}
}

func (u *udpProxy) gcLoop() {
	defer u.wg.Done()
	t := time.NewTicker(u.ttl / 2)
	if t.C == nil {
		t = time.NewTicker(15 * time.Second)
	}
	defer t.Stop()
	for {
		select {
		case <-u.ctx.Done():
			return
		case <-t.C:
			now := time.Now()
			u.sessions.Range(func(k, v any) bool {
				s := v.(*udpSession)
				if now.Sub(s.lastSeen) > u.ttl {
					_ = s.beConn.Close()
				}
				return true
			})
		}
	}
}