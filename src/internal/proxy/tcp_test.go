package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"lsh-utproxy/internal/balancer"
	"lsh-utproxy/internal/config"
	"lsh-utproxy/internal/stats"
)

func baseDefaults() config.Defaults {
	return config.Defaults{
		Mode:               "tcp",
		Balance:            "roundrobin",
		TimeoutConnect:     5 * time.Second,
		TimeoutClient:      30 * time.Second,
		TimeoutServer:      30 * time.Second,
		OptionTCPKeepAlive: true,
		TCPKeepAliveIdle:   30 * time.Second,
	}
}

func TestIdleTimeoutsHAProxyOrder(t *testing.T) {
	cases := []struct {
		name     string
		fe       config.Frontend
		be       config.Backend
		defs     config.Defaults
		wantCli  time.Duration
		wantServ time.Duration
	}{
		{
			name:    "tunnel wins over idle and client/server",
			fe:      config.Frontend{TimeoutTunnel: 45 * time.Minute, TimeoutIdle: 40 * time.Second, TimeoutClient: 30 * time.Second},
			be:      config.Backend{TimeoutIdle: 40 * time.Second, TimeoutServer: 30 * time.Second},
			defs:    baseDefaults(),
			wantCli: 45 * time.Minute, wantServ: 45 * time.Minute,
		},
		{
			name:    "idle applies when no tunnel timeout",
			fe:      config.Frontend{TimeoutIdle: 40 * time.Second, TimeoutClient: 30 * time.Second},
			be:      config.Backend{TimeoutIdle: 40 * time.Second, TimeoutServer: 30 * time.Second},
			defs:    baseDefaults(),
			wantCli: 40 * time.Second, wantServ: 40 * time.Second,
		},
		{
			name:    "client and server timeouts per direction",
			fe:      config.Frontend{TimeoutClient: 20 * time.Second},
			be:      config.Backend{TimeoutServer: 3 * time.Minute},
			defs:    config.Defaults{},
			wantCli: 20 * time.Second, wantServ: 3 * time.Minute,
		},
		{
			name:    "nothing configured keeps a long-lived tunnel",
			fe:      config.Frontend{},
			be:      config.Backend{},
			defs:    config.Defaults{},
			wantCli: time.Hour, wantServ: time.Hour,
		},
		{
			name:    "listen block overrides defaults for both directions",
			fe:      config.Frontend{TimeoutTunnel: 2 * time.Hour},
			be:      config.Backend{TimeoutTunnel: 2 * time.Hour},
			defs:    baseDefaults(),
			wantCli: 2 * time.Hour, wantServ: 2 * time.Hour,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &config.Config{Defaults: c.defs}
			bset := NewBackendSet(&c.be, nil, "tcp")
			gotCli, gotServ := idleTimeouts(cfg, c.fe, bset)
			if gotCli != c.wantCli || gotServ != c.wantServ {
				t.Fatalf("idle timeouts = (%v, %v), want (%v, %v)", gotCli, gotServ, c.wantCli, c.wantServ)
			}
		})
	}
}

// deadAddr returns an address that refuses connections.
func deadAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	a := ln.Addr().String()
	_ = ln.Close()
	return a
}

// echoBackend starts a TCP server that first writes a banner and then echoes
// everything back, like an RDP server that pushes graphics while the client
// sends input.
func echoBackend(t *testing.T, banner string) (addr string, conns func() int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var cnt atomic.Int64
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			cnt.Add(1)
			go func(c net.Conn) {
				defer c.Close()
				if banner != "" {
					if _, err := c.Write([]byte(banner)); err != nil {
						return
					}
				}
				b := make([]byte, 32*1024)
				for {
					n, err := c.Read(b)
					if n > 0 {
						if _, werr := c.Write(b[:n]); werr != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}(c)
		}
	}()
	return ln.Addr().String(), func() int64 { return cnt.Load() }
}

func testBackendSet(bcfg *config.Backend, addrs ...string) *BackendSet {
	slist := make([]balancer.Server, 0, len(addrs))
	for i, a := range addrs {
		scfg := config.Server{Name: fmt.Sprintf("s%d", i), Addr: a}
		if bcfg != nil {
			scfg.MaxConn = 0
		}
		slist = append(slist, NewBackendServer(scfg, bcfg))
	}
	return NewBackendSet(bcfg, slist, "tcp")
}

// End-to-end: banner from the backend plus a large echo stream at the same
// time, i.e. real bidirectional traffic through the proxy.
func TestHandleTCPBidirectionalSession(t *testing.T) {
	beAddr, _ := echoBackend(t, "HELLO")
	bcfg := &config.Backend{Name: "be", Mode: "tcp", TimeoutConnect: 3 * time.Second}
	bset := testBackendSet(bcfg, beAddr)

	cfg := &config.Config{Defaults: baseDefaults()}
	fe := config.Frontend{Name: "lsh-nb_tcp33390", Mode: "tcp", DefaultBackend: "be"}

	cliA, cliP := tcpPair(t)
	st := stats.New()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTCP(context.Background(), cfg, fe, cliP, map[string]*BackendSet{"be": bset}, st)
	}()

	banner := make([]byte, 5)
	_ = cliA.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := readFullDeadline(cliA, banner); err != nil {
		t.Fatalf("banner: %v", err)
	}
	if string(banner) != "HELLO" {
		t.Fatalf("banner corrupted: %q", banner)
	}

	const perStream = 200
	errc := make(chan error, 2)
	go func() { errc <- writeStream(cliA, perStream) }()
	go func() { errc <- readStream(cliA, perStream) }()

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("session: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("session stalled")
	}
	if tc, ok := cliA.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("handleTCP did not return after the client closed")
	}
	if got := st.ConnsOpen.Load(); got != 0 {
		t.Errorf("ConnsOpen leaked: %d", got)
	}
	if got := bset.srvs[0].Active(); got != 0 {
		t.Errorf("backend server active counter leaked: %d", got)
	}
	_ = cliA.Close()
}

// maxconn on a server must be respected exactly: no overshoot under concurrent
// connection attempts.
func TestHandleTCPServerMaxConn(t *testing.T) {
	beAddr, _ := echoBackend(t, "")
	bcfg := &config.Backend{Name: "be", Mode: "tcp", TimeoutConnect: 3 * time.Second}
	srv := NewBackendServer(config.Server{Name: "s0", Addr: beAddr, MaxConn: 2}, bcfg)
	bset := NewBackendSet(bcfg, []balancer.Server{srv}, "tcp")

	cfg := &config.Config{Defaults: baseDefaults()}
	fe := config.Frontend{Name: "fe", Mode: "tcp", DefaultBackend: "be"}
	st := stats.New()

	const attempts = 12
	accepted := make(chan struct{}, attempts)
	clientSides := make([]net.Conn, 0, attempts)
	for i := 0; i < attempts; i++ {
		cliA, cliP := tcpPair(t)
		clientSides = append(clientSides, cliA)
		go func(p net.Conn) {
			defer p.Close()
			handleTCP(context.Background(), cfg, fe, p, map[string]*BackendSet{"be": bset}, st)
			accepted <- struct{}{}
		}(cliP)
	}
	// Give the attempts time to race into the balancer.
	time.Sleep(500 * time.Millisecond)
	if got := srv.Active(); got > 2 {
		t.Fatalf("maxconn 2 exceeded: active=%d", got)
	}
	// Closing the client sides lets every session drain.
	for _, c := range clientSides {
		if tc, ok := c.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		_ = c.Close()
	}
	for i := 0; i < attempts; i++ {
		select {
		case <-accepted:
		case <-time.After(15 * time.Second):
			t.Fatalf("sessions did not drain")
		}
	}
	if got := srv.Active(); got != 0 {
		t.Errorf("active counter after drain: %d", got)
	}
}

// A backend that refuses the connection must not kill the client session: the
// proxy retries on the next server.
func TestHandleTCPRetryOnDeadBackend(t *testing.T) {
	beAddr, _ := echoBackend(t, "")
	dead := deadAddr(t)
	bcfg := &config.Backend{Name: "be", Mode: "tcp", TimeoutConnect: 2 * time.Second}
	// roundrobin starts at index 0: put the dead server first.
	bset := testBackendSet(bcfg, dead, beAddr)

	cfg := &config.Config{Defaults: baseDefaults()}
	fe := config.Frontend{Name: "fe", Mode: "tcp", DefaultBackend: "be"}

	cliA, cliP := tcpPair(t)
	st := stats.New()
	go handleTCP(context.Background(), cfg, fe, cliP, map[string]*BackendSet{"be": bset}, st)

	const perStream = 20
	errc := make(chan error, 2)
	go func() { errc <- writeStream(cliA, perStream) }()
	go func() { errc <- readStream(cliA, perStream) }()
	for i := 0; i < 2; i++ {
		select {
		case err := <-errc:
			if err != nil {
				t.Fatalf("session after dead backend: %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("session stalled")
		}
	}
	if tc, ok := cliA.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
	_ = cliA.Close()
}

// A missing backend must close the client connection instead of leaving it
// hanging.
func TestHandleTCPNoBackend(t *testing.T) {
	cfg := &config.Config{Defaults: baseDefaults()}
	fe := config.Frontend{Name: "fe", Mode: "tcp", DefaultBackend: "nope"}
	cliA, cliP := tcpPair(t)
	st := stats.New()
	handleTCP(context.Background(), cfg, fe, cliP, map[string]*BackendSet{}, st)
	b := make([]byte, 8)
	_ = cliA.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := cliA.Read(b)
	if n != 0 || err == nil {
		t.Fatalf("client connection not closed on missing backend: n=%d err=%v", n, err)
	}
	_ = cliA.Close()
}

func readFullDeadline(c net.Conn, b []byte) (int, error) {
	total := 0
	for total < len(b) {
		n, err := c.Read(b[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// Health checks must honor the per-server "inter" and take a dead server out of
// rotation after "fall" probes.
func TestHealthCheckTakesDeadServerOutOfRotation(t *testing.T) {
	dead := deadAddr(t)
	bcfg := &config.Backend{Name: "be", Mode: "tcp", TimeoutCheck: 300 * time.Millisecond}
	srv := NewBackendServer(config.Server{
		Name:  "dead",
		Addr:  dead,
		Check: true,
		Inter: 100 * time.Millisecond,
		Fall:  2,
		Rise:  1,
	}, bcfg)
	bset := NewBackendSet(bcfg, []balancer.Server{srv}, "tcp")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go healthCheckLoop(ctx, []*BackendServer{srv}, "tcp")

	deadline := time.Now().Add(3 * time.Second)
	for srv.Up() {
		if time.Now().After(deadline) {
			t.Fatalf("server still up after 3s of failing checks (inter=100ms, fall=2)")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := bset.Next("1.2.3.4"); !errors.Is(err, balancer.ErrNoHealthy) {
		t.Errorf("balancer still hands out a downed server: %v", err)
	}
}

// A healthy server must not be marked down, and its probes must not run faster
// than "inter".
func TestHealthCheckKeepsLiveServerUp(t *testing.T) {
	beAddr, conns := echoBackend(t, "")
	bcfg := &config.Backend{Name: "be", Mode: "tcp", TimeoutCheck: 300 * time.Millisecond}
	srv := NewBackendServer(config.Server{
		Name:  "live",
		Addr:  beAddr,
		Check: true,
		Inter: 200 * time.Millisecond,
		Fall:  2,
		Rise:  1,
	}, bcfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go healthCheckLoop(ctx, []*BackendServer{srv}, "tcp")

	time.Sleep(1500 * time.Millisecond)
	if !srv.Up() {
		t.Fatalf("live server was marked down")
	}
	if got := conns(); got > 8 {
		t.Errorf("health checks too frequent: %d connections in 1.5s with inter=200ms", got)
	}
}
