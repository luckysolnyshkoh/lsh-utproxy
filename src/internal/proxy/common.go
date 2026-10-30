package proxy

import (
	"context"
	"errors"
	"io"
	"lsh-utproxy/internal/balancer"
	"lsh-utproxy/internal/config"
	"lsh-utproxy/internal/stats"
	"lsh-utproxy/internal/util"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

var ErrServerFull = errors.New("server maxconn reached")

type BackendServer struct {
	id      string
	addr    string
	weight  int
	maxConn int
	backup  bool
	active  atomic.Int64
	up      atomic.Bool
	rise    int
	fall    int
	riseCnt atomic.Int32
	fallCnt atomic.Int32
	check   bool
	inter   time.Duration
	checkTO time.Duration
}

func NewBackendServer(sc config.Server, bcfg *config.Backend) *BackendServer {
	bs := &BackendServer{
		id:      sc.Name,
		addr:    sc.Addr,
		weight:  sc.Weight,
		maxConn: sc.MaxConn,
		backup:  sc.Backup,
		rise:    sc.Rise,
		fall:    sc.Fall,
		check:   sc.Check,
		inter:   sc.Inter,
		checkTO: bcfg.TimeoutCheck,
	}
	if bs.rise <= 0 {
		bs.rise = bcfg.CheckRise
		if bs.rise <= 0 {
			bs.rise = 2
		}
	}
	if bs.fall <= 0 {
		bs.fall = bcfg.CheckFall
		if bs.fall <= 0 {
			bs.fall = 3
		}
	}
	if bs.checkTO <= 0 {
		bs.checkTO = 2 * time.Second
	}
	if bs.inter <= 0 {
		bs.inter = bcfg.CheckInterval
		if bs.inter <= 0 {
			bs.inter = 2 * time.Second
		}
	}
	bs.up.Store(true)
	if bs.weight <= 0 {
		bs.weight = 1
	}
	return bs
}

func (s *BackendServer) ID() string       { return s.id }
func (s *BackendServer) Addr() string     { return s.addr }
func (s *BackendServer) Weight() int      { return s.weight }
func (s *BackendServer) MaxConn() int     { return s.maxConn }
func (s *BackendServer) Backup() bool     { return s.backup }
func (s *BackendServer) Active() int64    { return s.active.Load() }
func (s *BackendServer) Inc() int64       { return s.active.Add(1) }
func (s *BackendServer) Dec() int64       { return s.active.Add(-1) }
func (s *BackendServer) Up() bool         { return s.up.Load() }
func (s *BackendServer) SetUp(v bool)     { s.up.Store(v) }
func (s *BackendServer) HealthScore() int { return 0 }

// TryInc reserves one connection slot against limit (0 = unlimited).
// It is atomic, so concurrent connections cannot both pass a maxconn check
// and then overshoot the limit.
func (s *BackendServer) TryInc(limit int) bool {
	if limit <= 0 {
		s.active.Add(1)
		return true
	}
	for {
		cur := s.active.Load()
		if cur >= int64(limit) {
			return false
		}
		if s.active.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

func (s *BackendServer) CheckTCP(ctx context.Context) bool {
	d := net.Dialer{Timeout: s.checkTO}
	c, err := d.DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func (s *BackendServer) CheckUDP(ctx context.Context) bool {
	d := net.Dialer{Timeout: s.checkTO}
	c, err := d.DialContext(ctx, "udp", s.addr)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// copyTimeouts holds the inactivity timers of one proxied session, per side,
// following HAProxy TCP-mode semantics: each direction has its own idle timer.
type copyTimeouts struct {
	clientIdle time.Duration // inactivity on the client connection
	serverIdle time.Duration // inactivity on the backend connection
	writeTO    time.Duration // how long a single blocked write may take
}

const defaultWriteTO = 10 * time.Second

// dirOutcome is written by exactly one goroutine and read after wg.Wait.
type dirOutcome struct {
	n   int64
	err error
}

// copyBidir pumps traffic in both directions until both directions end.
//
// Each direction uses its own buffer: one buffer shared by the two goroutines
// lets a read from one side overwrite the bytes the other side is still
// writing, which corrupts the forwarded stream.
//
// A clean FIN in one direction half-closes the opposite side and the session
// keeps running (HAProxy TCP behaviour). Any other failure aborts both
// directions at once so no goroutine is left blocked until the idle timer.
func copyBidir(beConn, cliConn net.Conn, st *stats.Stats, to copyTimeouts) (in, out int64, errIn, errOut error) {
	if to.writeTO <= 0 {
		to.writeTO = defaultWriteTO
	}
	var once sync.Once
	abort := func() {
		once.Do(func() {
			_ = cliConn.Close()
			_ = beConn.Close()
		})
	}

	var wg sync.WaitGroup
	var rin, rout dirOutcome
	wg.Add(2)
	go func() {
		defer wg.Done()
		buf := util.GetBuf()
		defer util.PutBuf(buf)
		rin.n, rin.err = copyLoop(beConn, cliConn, buf, to.clientIdle, to.writeTO, st, false)
		if isEOF(rin.err) {
			tryCloseWrite(beConn)
		} else {
			abort()
		}
	}()
	go func() {
		defer wg.Done()
		buf := util.GetBuf()
		defer util.PutBuf(buf)
		rout.n, rout.err = copyLoop(cliConn, beConn, buf, to.serverIdle, to.writeTO, st, true)
		if isEOF(rout.err) {
			tryCloseWrite(cliConn)
		} else {
			abort()
		}
	}()
	wg.Wait()
	return rin.n, rout.n, rin.err, rout.err
}

func tryCloseWrite(c net.Conn) {
	if tc, ok := c.(interface{ CloseWrite() error }); ok {
		_ = tc.CloseWrite()
	}
}

func isEOF(err error) bool {
	return err == nil || errors.Is(err, io.EOF)
}

func copyLoop(dst, src net.Conn, buf *[]byte, idleTO, writeTO time.Duration, st *stats.Stats, out bool) (int64, error) {
	b := *buf
	var total int64
	for {
		if idleTO > 0 {
			_ = src.SetReadDeadline(time.Now().Add(idleTO))
		}
		n, err := src.Read(b)
		if n > 0 {
			total += int64(n)
			if st != nil {
				if out {
					st.BytesOut.Add(int64(n))
				} else {
					st.BytesIn.Add(int64(n))
				}
			}
			if writeTO > 0 {
				_ = dst.SetWriteDeadline(time.Now().Add(writeTO))
			}
			if _, werr := dst.Write(b[:n]); werr != nil {
				return total, werr
			}
		}
		if err != nil {
			return total, err
		}
	}
}

// connLimiter caps the number of connections handled at the same time.
// A nil limiter or limit <= 0 means unlimited.
type connLimiter struct {
	limit int
	cur   atomic.Int64
}

func (l *connLimiter) tryAcquire() bool {
	if l == nil || l.limit <= 0 {
		return true
	}
	for {
		cur := l.cur.Load()
		if cur >= int64(l.limit) {
			return false
		}
		if l.cur.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

func (l *connLimiter) release() {
	if l == nil || l.limit <= 0 {
		return
	}
	l.cur.Add(-1)
}

type BackendSet struct {
	bcfg      *config.Backend
	srvs      []balancer.Server
	bal       balancer.Balancer
	mode      string
	lastNoSrv atomic.Int64 // unix nanos of the last "no server available" log line
}

// shouldLogNoServer limits the "all servers are down" warning to one line per
// five seconds per backend: on a busy port this would otherwise be logged for
// every refused connection.
func (bs *BackendSet) shouldLogNoServer() bool {
	now := time.Now().UnixNano()
	last := bs.lastNoSrv.Load()
	if now-last < int64(5*time.Second) {
		return false
	}
	return bs.lastNoSrv.CompareAndSwap(last, now)
}

func NewBackendSet(bcfg *config.Backend, servers []balancer.Server, mode string) *BackendSet {
	bs := &BackendSet{bcfg: bcfg, srvs: servers, mode: mode}
	bs.bal = balancer.New(bcfg, servers)
	return bs
}

func (bs *BackendSet) Next(key string) (balancer.Server, error) { return bs.bal.Next(key) }

// healthCheckLoop dials every server that has "check" enabled, each on its own
// "inter" interval, and flips a server up/down after "rise"/"fall" consecutive
// good or bad probes. The balancers skip servers that are down, which is what
// keeps traffic away from a dead backend.
func healthCheckLoop(ctx context.Context, srvs []*BackendServer, mode string) {
	const tick = time.Second
	t := time.NewTicker(tick)
	defer t.Stop()

	// nextRun[s] is when s is probed next. First probes are staggered so a
	// backend does not receive a burst of simultaneous connections at startup.
	nextRun := make(map[*BackendServer]time.Time, len(srvs))
	start := time.Now()
	for i, s := range srvs {
		if !s.check {
			continue
		}
		if s.inter <= 0 {
			s.inter = 2 * time.Second
		}
		nextRun[s] = start.Add(time.Duration(i) * tick)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := time.Now()
			for _, s := range srvs {
				nxt, ok := nextRun[s]
				if !ok || now.Before(nxt) {
					continue
				}
				nextRun[s] = now.Add(s.inter)
				s.runCheck(ctx, mode)
			}
		}
	}
}

// runCheck performs one probe and applies the rise/fall state machine.
func (s *BackendServer) runCheck(ctx context.Context, mode string) {
	hctx, cancel := context.WithTimeout(ctx, s.checkTO)
	defer cancel()

	ok := false
	if mode == "udp" {
		ok = s.CheckUDP(hctx)
	} else {
		ok = s.CheckTCP(hctx)
	}

	if ok {
		s.fallCnt.Store(0)
		if !s.up.Load() {
			rc := s.riseCnt.Add(1)
			if int(rc) >= s.rise {
				s.up.Store(true)
				log.Info().Str("server", s.id).Str("addr", s.addr).Msg("server up")
			}
		} else {
			s.riseCnt.Store(0)
		}
	} else {
		s.riseCnt.Store(0)
		if s.up.Load() {
			fc := s.fallCnt.Add(1)
			if int(fc) >= s.fall {
				s.up.Store(false)
				log.Warn().Str("server", s.id).Str("addr", s.addr).Msg("server down")
			}
		} else {
			s.fallCnt.Store(0)
		}
	}
}

func isCloseErr(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	s := err.Error()
	if strings.Contains(s, "use of closed network connection") {
		return true
	}
	if strings.Contains(s, "broken pipe") {
		return true
	}
	if strings.Contains(s, "connection reset by peer") {
		return true
	}
	return false
}

func isIdleTimeout(err error) bool {
	return errors.Is(err, os.ErrDeadlineExceeded)
}
