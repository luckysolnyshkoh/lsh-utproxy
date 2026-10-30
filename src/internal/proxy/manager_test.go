package proxy

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"lsh-utproxy/internal/config"
)

// freePortPair finds a port that is free for both TCP and UDP. Windows
// reserves large ranges of dynamic ports (WinNAT/Hyper-V), so a port that is
// free for TCP can be refused for UDP; retry and skip if nothing works.
func freePortPair(t *testing.T) int {
	t.Helper()
	for attempt := 0; attempt < 8; attempt++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("probe listen: %v", err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()

		pc, uerr := net.ListenPacket("udp", "127.0.0.1:"+strconv.Itoa(port))
		if uerr != nil {
			continue
		}
		_ = pc.Close()
		return port
	}
	t.Skip("no TCP+UDP port pair available in this environment")
	return 0
}

func parseConfig(t *testing.T, src string) *config.Config {
	t.Helper()
	cfg, err := config.NewParser(strings.NewReader(src)).Config()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return cfg
}

// The proxy must bind before serving and stop promptly when the context is
// canceled: that is how systemd and the Windows SCM stop it.
func TestRunWithCtxBindsAndStops(t *testing.T) {
	port := freePortPair(t)
	cfg := parseConfig(t, `
global
  log stderr level none
  grace shutdown 3s

defaults
  mode tcp
  balance roundrobin
  timeout connect 2s
  timeout tunnel 60s

listen t_tcp
  mode tcp
  bind 127.0.0.1:`+strconv.Itoa(port)+`
  server s1 127.0.0.1:9

listen t_udp
  mode udp
  bind 127.0.0.1:`+strconv.Itoa(port)+`
  udp-session-ttl 5s
  server u1 127.0.0.1:9
`)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunWithCtx(ctx, cfg) }()

	var conn net.Conn
	var err error
	for i := 0; i < 40; i++ {
		conn, err = net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		cancel()
		<-done
		t.Fatalf("tcp port %d was not accepted: %v", port, err)
	}
	// The backend is unreachable (127.0.0.1:9), so the proxy must close us.
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	b := make([]byte, 4)
	if n, rerr := conn.Read(b); n != 0 && rerr == nil {
		t.Errorf("unexpected %d bytes from an unreachable backend", n)
	}
	_ = conn.Close()

	// The UDP socket must be bound: sending to it must not be refused.
	uc, uerr := net.Dial("udp", "127.0.0.1:"+strconv.Itoa(port))
	if uerr != nil {
		cancel()
		<-done
		t.Fatalf("udp dial: %v", uerr)
	}
	if _, werr := uc.Write([]byte("ping")); werr != nil {
		t.Errorf("udp write: %v", werr)
	}
	_ = uc.Close()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("RunWithCtx returned error: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("RunWithCtx did not stop after cancel")
	}
}

// A port already taken must fail the whole start with a readable error instead
// of leaving a half-running proxy.
func TestRunWithCtxReportsBindFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	cfg := parseConfig(t, `
global
  log stderr level none
defaults
  mode tcp
listen dup
  mode tcp
  bind 127.0.0.1:`+strconv.Itoa(port)+`
  server s1 127.0.0.1:9
`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = RunWithCtx(ctx, cfg)
	if err == nil {
		t.Fatalf("expected a bind error, got none")
	}
	if !strings.Contains(err.Error(), "bind tcp") {
		t.Errorf("unexpected error text: %v", err)
	}
}

func TestParseRejectsBadBind(t *testing.T) {
	src := "listen bad\n  mode tcp\n  bind 1.2.3.4:99999\n  server s 10.0.0.1:80\n"
	if _, err := config.NewParser(strings.NewReader(src)).Config(); err == nil {
		t.Fatalf("bad bind accepted")
	}
}
