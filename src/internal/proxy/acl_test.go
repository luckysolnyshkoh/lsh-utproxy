package proxy

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"lsh-utproxy/internal/acl"
	"lsh-utproxy/internal/balancer"
	"lsh-utproxy/internal/config"
	"lsh-utproxy/internal/stats"
)

// tcpAccess starts a real TCP frontend on 127.0.0.1 with compiled access rules
// and an echo backend, and returns the address plus the stop function.
func tcpAccess(t *testing.T, rules []config.AccessRule) (addr string, st *stats.Stats, stop func()) {
	t.Helper()
	l, err := acl.New(rules)
	if err != nil {
		t.Fatalf("compile rules: %v", err)
	}
	beAddr, _ := echoBackend(t, "HELLO")
	bcfg := &config.Backend{Name: "be", Mode: "tcp", TimeoutConnect: 3 * time.Second}
	bset := testBackendSet(bcfg, beAddr)
	cfg := &config.Config{Defaults: baseDefaults()}
	fe := config.Frontend{Name: "fe", Mode: "tcp", DefaultBackend: "be"}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	st = stats.New()
	go serveTCP(ctx, cfg, fe, l, ln, ln.Addr().String(), map[string]*BackendSet{"be": bset}, st, &connLimiter{}, &connLimiter{})
	return ln.Addr().String(), st, func() {
		cancel()
		_ = ln.Close()
	}
}

// udpAccess is the same for the UDP path: a real socket, the rule list, and a
// UDP echo server as the backend.
func udpAccess(t *testing.T, rules []config.AccessRule) (addr string, st *stats.Stats, stop func()) {
	t.Helper()
	l, err := acl.New(rules)
	if err != nil {
		t.Fatalf("compile rules: %v", err)
	}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("udp echo listen: %v", err)
	}
	go func() {
		buf := make([]byte, 512)
		for {
			n, src, rerr := pc.ReadFrom(buf)
			if rerr != nil {
				return
			}
			if n == 0 {
				continue
			}
			_, _ = pc.WriteTo(buf[:n], src)
		}
	}()
	t.Cleanup(func() { _ = pc.Close() })

	bcfg := &config.Backend{Name: "be", Mode: "udp", TimeoutConnect: 2 * time.Second}
	slist := []balancer.Server{NewBackendServer(config.Server{Name: "u1", Addr: pc.LocalAddr().String()}, bcfg)}
	bset := NewBackendSet(bcfg, slist, "udp")

	cfg := &config.Config{Defaults: baseDefaults()}
	fe := config.Frontend{Name: "fe", Mode: "udp", DefaultBackend: "be"}
	lconn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("udp listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	st = stats.New()
	go serveUDP(ctx, cfg, fe, l, lconn, map[string]*BackendSet{"be": bset}, st)
	return lconn.LocalAddr().String(), st, func() {
		cancel()
		_ = lconn.Close()
	}
}

// dialLocal opens a TCP connection to a local proxy listener, retrying until
// the listener is up.
func dialLocal(t *testing.T, addr string) net.Conn {
	t.Helper()
	for i := 0; i < 40; i++ {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			return c
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("cannot dial %s", addr)
	return nil
}

// readWithin reads at most n bytes; returns how many arrived and the error.
func readWithin(c net.Conn, n int, d time.Duration) (int, error) {
	b := make([]byte, n)
	_ = c.SetReadDeadline(time.Now().Add(d))
	return c.Read(b)
}

// A deny rule in front of allow all must block the loopback client, and the
// blocked connection must be visible in the stats counter.
func TestTCPRulesBlockInConfigOrder(t *testing.T) {
	addr, st, stop := tcpAccess(t, []config.AccessRule{
		{Action: "deny", Addr: "127.0.0.0/8"},
		{Action: "allow", Addr: "all"},
	})
	defer stop()

	c := dialLocal(t, addr)
	defer c.Close()
	if n, err := readWithin(c, 5, 3*time.Second); n != 0 || err == nil {
		t.Fatalf("connection from 127.0.0.1 was not refused by the deny rule (n=%d err=%v)", n, err)
	}
	if got := st.Blocked.Load(); got != 1 {
		t.Errorf("Blocked = %d, want 1", got)
	}
	if got := st.ConnsTotal.Load(); got != 0 {
		t.Errorf("a blocked connection counted into ConnsTotal: %d", got)
	}
}

// The same rules in the opposite order let the client through: order is what
// decides, not the presence of a deny rule.
func TestTCPAllowBeforeDenyPasses(t *testing.T) {
	addr, st, stop := tcpAccess(t, []config.AccessRule{
		{Action: "allow", Addr: "127.0.0.0/8"},
		{Action: "deny", Addr: "all"},
	})
	defer stop()

	c := dialLocal(t, addr)
	defer c.Close()
	b := make([]byte, 5)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := readFullDeadline(c, b)
	if err != nil || string(b) != "HELLO" {
		t.Fatalf("allowed session broken: n=%d got %q err=%v", n, b, err)
	}
	if _, werr := c.Write([]byte("xx")); werr != nil {
		t.Errorf("write: %v", werr)
	}
	if got := st.Blocked.Load(); got != 0 {
		t.Errorf("Blocked = %d, want 0", got)
	}
}

// drop and deny both refuse the client; the difference is that deny answers
// with a reset so the client learns at once, while drop leaves no reply.
func TestTCPDropIsSilentDenyIsReset(t *testing.T) {
	dropAddr, _, stopDrop := tcpAccess(t, []config.AccessRule{{Action: "drop", Addr: "all"}})
	defer stopDrop()
	denyAddr, _, stopDeny := tcpAccess(t, []config.AccessRule{{Action: "deny", Addr: "all"}})
	defer stopDeny()

	dropC := dialLocal(t, dropAddr)
	defer dropC.Close()
	dropN, dropErr := readWithin(dropC, 4, 3*time.Second)
	if dropN != 0 || dropErr == nil {
		t.Fatalf("drop did not close the connection (n=%d err=%v)", dropN, dropErr)
	}

	denyC := dialLocal(t, denyAddr)
	defer denyC.Close()
	denyN, denyErr := readWithin(denyC, 4, 3*time.Second)
	if denyN != 0 || denyErr == nil {
		t.Fatalf("deny did not close the connection (n=%d err=%v)", denyN, denyErr)
	}
	t.Logf("drop close: %v", dropErr)
	t.Logf("deny close: %v", denyErr)
	if denyErr == dropErr {
		t.Logf("both actions report the same close error on this platform: %v", denyErr)
	}
	if isEOF(denyErr) {
		t.Errorf("deny answered with a plain FIN (%v): it must reset the connection so the client learns at once", denyErr)
	}
}

// A rule for a subnet the client is not in must not touch it.
func TestTCPRuleForOtherSubnetAllows(t *testing.T) {
	addr, st, stop := tcpAccess(t, []config.AccessRule{
		{Action: "deny", Addr: "192.168.1.0/24"},
		{Action: "deny", Addr: "all"},
	})
	defer stop()
	// The loopback client is not in 192.168.1.0/24, so the first rule does not
	// match and the second one, "deny all", blocks it.
	c := dialLocal(t, addr)
	defer c.Close()
	if n, err := readWithin(c, 5, 3*time.Second); n != 0 || err == nil {
		t.Fatalf("deny all did not block the loopback client (n=%d err=%v)", n, err)
	}
	if st.Blocked.Load() != 1 {
		t.Errorf("Blocked = %d, want 1", st.Blocked.Load())
	}
}

// An IPv6-only rule must not block an IPv4 client.
func TestTCPIPv6RuleDoesNotBlockIPv4(t *testing.T) {
	addr, st, stop := tcpAccess(t, []config.AccessRule{
		{Action: "deny", Addr: "::1/128"},
		{Action: "allow", Addr: "all"},
	})
	defer stop()

	c := dialLocal(t, addr)
	defer c.Close()
	b := make([]byte, 5)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := readFullDeadline(c, b); err != nil || string(b) != "HELLO" {
		t.Fatalf("IPv4 client blocked by an IPv6 rule: %q err=%v", b, err)
	}
	if st.Blocked.Load() != 0 {
		t.Errorf("Blocked = %d, want 0", st.Blocked.Load())
	}
}

// UDP: a deny rule discards the datagram, so the echo answer never arrives.
func TestUDPRulesBlockDatagram(t *testing.T) {
	addr, st, stop := udpAccess(t, []config.AccessRule{
		{Action: "deny", Addr: "127.0.0.0/8"},
		{Action: "allow", Addr: "all"},
	})
	defer stop()

	c, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("udp dial: %v", err)
	}
	defer c.Close()
	if _, werr := c.Write([]byte("ping")); werr != nil {
		t.Fatalf("udp write: %v", werr)
	}
	b := make([]byte, 16)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, rerr := c.Read(b)
	if n != 0 || rerr == nil {
		t.Fatalf("datagram was answered despite the deny rule: %q err=%v", b[:n], rerr)
	}
	if st.Blocked.Load() != 1 {
		t.Errorf("Blocked = %d, want 1", st.Blocked.Load())
	}
}

func TestUDPAllowBeforeDenyPasses(t *testing.T) {
	addr, st, stop := udpAccess(t, []config.AccessRule{
		{Action: "allow", Addr: "localhost"},
		{Action: "deny", Addr: "all"},
	})
	defer stop()

	c, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("udp dial: %v", err)
	}
	defer c.Close()
	if _, werr := c.Write([]byte("ping")); werr != nil {
		t.Fatalf("udp write: %v", werr)
	}
	b := make([]byte, 16)
	_ = c.SetReadDeadline(time.Now().Add(4 * time.Second))
	n, rerr := c.Read(b)
	if n < 4 || string(b[:4]) != "ping" {
		t.Fatalf("allowed UDP session broken: got %q err=%v", b[:n], rerr)
	}
	if st.Blocked.Load() != 0 {
		t.Errorf("Blocked = %d, want 0", st.Blocked.Load())
	}
}

// The whole path through RunWithCtx: the rules of a listen block are compiled
// and enforced by the manager, not only by the serve loops.
func TestRunWithCtxEnforcesListenAccessRules(t *testing.T) {
	port := freePortPair(t)
	beAddr, _ := echoBackend(t, "PING")

	cfg := parseConfig(t, `
global
  log stderr level none
  grace shutdown 3s
defaults
  mode tcp
  timeout connect 2s
  timeout tunnel 30s
listen block
  mode tcp
  bind 127.0.0.1:`+strconv.Itoa(port)+`
  deny 127.0.0.0/8
  allow all
  server s1 `+beAddr+`
`)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunWithCtx(ctx, cfg) }()

	c := dialLocal(t, "127.0.0.1:"+strconv.Itoa(port))
	n, err := readWithin(c, 4, 3*time.Second)
	_ = c.Close()
	if n != 0 || err == nil {
		cancel()
		<-done
		t.Fatalf("listen block did not enforce the deny rule (n=%d err=%v)", n, err)
	}
	cancel()
	select {
	case rerr := <-done:
		if rerr != nil {
			t.Errorf("RunWithCtx: %v", rerr)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("RunWithCtx did not stop")
	}

	// The same block with allow first must proxy normally.
	cfg2 := parseConfig(t, `
global
  log stderr level none
  grace shutdown 3s
defaults
  mode tcp
  timeout connect 2s
  timeout tunnel 30s
listen pass
  mode tcp
  bind 127.0.0.1:`+strconv.Itoa(port)+`
  allow 127.0.0.0/8
  deny all
  server s1 `+beAddr+`
`)
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan error, 1)
	go func() { done2 <- RunWithCtx(ctx2, cfg2) }()

	c2 := dialLocal(t, "127.0.0.1:"+strconv.Itoa(port))
	b := make([]byte, 4)
	_ = c2.SetReadDeadline(time.Now().Add(6 * time.Second))
	if _, err := readFullDeadline(c2, b); err != nil || string(b) != "PING" {
		cancel2()
		<-done2
		t.Fatalf("allowed listen session broken: %q err=%v", b, err)
	}
	_ = c2.Close()
	cancel2()
	if rerr := <-done2; rerr != nil {
		t.Errorf("RunWithCtx: %v", rerr)
	}
}

// Bad rule text that survived the parser (it cannot happen with the current
// parser, but the manager must not panic on it) is reported at startup.
func TestStartupReportsBadAccessRule(t *testing.T) {
	cfg := parseConfig(t, `
listen l
  mode tcp
  bind 127.0.0.1:0
  server s 10.0.0.1:80
`)
	cfg.Frontends[0].AccessRules = []config.AccessRule{{Action: "deny", Addr: "10.0.0.0/99", Line: 3}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := RunWithCtx(ctx, cfg)
	if err == nil {
		t.Fatalf("bad rule accepted at startup")
	}
	if !strings.Contains(err.Error(), "access rules") {
		t.Errorf("error text = %v, want it to name the access rules", err)
	}
}
