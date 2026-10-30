package proxy

import (
	"context"
	"errors"
	"io"
	"lsh-utproxy/internal/balancer"
	"lsh-utproxy/internal/config"
	"lsh-utproxy/internal/stats"
	"net"
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
func (s *BackendServer) Backup() bool      { return s.backup }
func (s *BackendServer) Active() int64    { return s.active.Load() }
func (s *BackendServer) Inc() int64        { return s.active.Add(1) }
func (s *BackendServer) Dec() int64        { return s.active.Add(-1) }
func (s *BackendServer) Up() bool         { return s.up.Load() }
func (s *BackendServer) SetUp(v bool)      { s.up.Store(v) }
func (s *BackendServer) HealthScore() int { return 0 }

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

func copyBidir(ctx context.Context, dst, src net.Conn, buf *[]byte, st *stats.Stats, idleTO time.Duration) (n1, n2 int64, err1, err2 error) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		n1, err1 = copyWithTO(ctx, dst, src, buf, idleTO, st, false)
		tryCloseWrite(dst)
	}()
	go func() {
		defer wg.Done()
		n2, err2 = copyWithTO(ctx, src, dst, buf, idleTO, st, true)
		tryCloseWrite(src)
	}()
	wg.Wait()
	if err1 == io.EOF {
		err1 = nil
	}
	if err2 == io.EOF {
		err2 = nil
	}
	return
}

func tryCloseWrite(c net.Conn) {
	if tc, ok := c.(interface{ CloseWrite() error }); ok {
		_ = tc.CloseWrite()
	}
}

func copyWithTO(ctx context.Context, dst, src net.Conn, buf *[]byte, idleTO time.Duration, st *stats.Stats, out bool) (int64, error) {
	b := *buf
	var total int64
	for {
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}
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
			if idleTO > 0 {
				_ = dst.SetWriteDeadline(time.Now().Add(10 * time.Second))
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

type BackendSet struct {
	bcfg *config.Backend
	srvs []balancer.Server
	bal  balancer.Balancer
	mode string
}

func NewBackendSet(bcfg *config.Backend, servers []balancer.Server, mode string) *BackendSet {
	bs := &BackendSet{bcfg: bcfg, srvs: servers, mode: mode}
	bs.bal = balancer.New(bcfg, servers)
	return bs
}

func (bs *BackendSet) Next() (balancer.Server, error) { return bs.bal.Next() }

func healthCheckLoop(ctx context.Context, srvs []*BackendServer, mode string) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, s := range srvs {
				if !s.check {
					continue
				}
				ok := false
				hctx, hc := context.WithTimeout(context.Background(), s.checkTO)
				if mode == "udp" {
					ok = s.CheckUDP(hctx)
				} else {
					ok = s.CheckTCP(hctx)
				}
				hc()
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