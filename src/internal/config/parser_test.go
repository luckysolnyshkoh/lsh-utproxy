package config

import (
	"strings"
	"testing"
	"time"
)

const rdpListen = `
global
  log stdout format json level info
  maxconn 65535
  grace shutdown 10s

defaults
  mode tcp
  balance roundrobin
  timeout connect 5s
  timeout client 30s
  timeout server 30s
  timeout tunnel 3600s
  option tcpkeepalive

listen lsh-nb_tcp33390
  mode tcp
  bind 178.159.38.221:33390
  balance roundrobin
  server lsh-nb_local 10.9.9.3:3389
`

func TestParseRDPListenBlock(t *testing.T) {
	cfg, err := NewParser(strings.NewReader(rdpListen)).Config()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cfg.Frontends) != 1 || len(cfg.Backends) != 1 {
		t.Fatalf("want 1 frontend + 1 backend, got %d/%d", len(cfg.Frontends), len(cfg.Backends))
	}
	fe := cfg.Frontends[0]
	if fe.Name != "lsh-nb_tcp33390" {
		t.Errorf("frontend name = %q", fe.Name)
	}
	if fe.Mode != "tcp" {
		t.Errorf("frontend mode = %q, want tcp", fe.Mode)
	}
	if fe.DefaultBackend != "lsh-nb_tcp33390" {
		t.Errorf("default_backend = %q", fe.DefaultBackend)
	}
	if len(fe.Bind) != 1 || fe.Bind[0].Addr != "178.159.38.221:33390" || fe.Bind[0].Port != 33390 {
		t.Errorf("bind = %+v", fe.Bind)
	}
	be := cfg.Backends[0]
	if be.Mode != "tcp" {
		t.Errorf("backend mode = %q, want tcp", be.Mode)
	}
	if be.Balance != "roundrobin" {
		t.Errorf("balance = %q", be.Balance)
	}
	if len(be.Servers) != 1 {
		t.Fatalf("servers = %d", len(be.Servers))
	}
	s := be.Servers[0]
	if s.Name != "lsh-nb_local" || s.Addr != "10.9.9.3:3389" {
		t.Errorf("server = %+v", s)
	}
	if s.Weight != 1 {
		t.Errorf("server weight = %d, want 1", s.Weight)
	}
	// The timeouts come from "defaults": the parser has no built-in idle/tunnel
	// default, so the copy engine resolves them, and what is configured here has
	// to survive parsing.
	d := cfg.Defaults
	if d.TimeoutTunnel != 3600*time.Second {
		t.Errorf("defaults timeout tunnel = %v", d.TimeoutTunnel)
	}
	if d.TimeoutClient != 30*time.Second || d.TimeoutServer != 30*time.Second {
		t.Errorf("defaults timeout client/server = %v / %v", d.TimeoutClient, d.TimeoutServer)
	}
	if d.TimeoutConnect != 5*time.Second {
		t.Errorf("defaults timeout connect = %v", d.TimeoutConnect)
	}
	if d.TimeoutIdle != 0 {
		t.Errorf("defaults timeout idle = %v, want 0 (not configured)", d.TimeoutIdle)
	}
	if be.TimeoutTunnel != 0 || be.TimeoutIdle != 0 {
		t.Errorf("listen block timeouts leaked from defaults: %v / %v", be.TimeoutTunnel, be.TimeoutIdle)
	}
	if !cfg.Defaults.OptionTCPKeepAlive {
		t.Errorf("option tcpkeepalive not applied to defaults")
	}
	if cfg.Global.MaxConn != 65535 {
		t.Errorf("global maxconn = %d", cfg.Global.MaxConn)
	}
}

func TestServerParams(t *testing.T) {
	src := `
listen lb
  mode tcp
  bind 0.0.0.0:80
  balance leastconn
  server a 10.0.0.1:80 check maxconn 100 weight 3 inter 5s fall 2 rise 1 backup
  server b 10.0.0.2:80 check
`
	cfg, err := NewParser(strings.NewReader(src)).Config()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	be := cfg.Backends[0]
	if len(be.Servers) != 2 {
		t.Fatalf("servers = %d", len(be.Servers))
	}
	a := be.Servers[0]
	if !a.Check || !a.Backup || a.MaxConn != 100 || a.Weight != 3 || a.Inter != 5*time.Second || a.Fall != 2 || a.Rise != 1 {
		t.Errorf("server a params lost: %+v", a)
	}
	if !be.Servers[1].Check {
		t.Errorf("server b: check lost")
	}
}

func TestBindForms(t *testing.T) {
	for _, tc := range []struct {
		in, addr string
		port     int
	}{
		{"0.0.0.0:80", "0.0.0.0:80", 80},
		{"*:8080", "0.0.0.0:8080", 8080},
		{"178.159.38.221:33390", "178.159.38.221:33390", 33390},
		{"[::]:3389", "[::]:3389", 3389},
		{"::3389", "[::]:3389", 3389},
		{":9090", "0.0.0.0:9090", 9090},
		{"rdp1.example.com:3389", "rdp1.example.com:3389", 3389},
		{"localhost:14141", "localhost:14141", 14141},
		{"[fe80::1%eth0]:80", "", 0},
	} {
		b, err := parseBindStr(tc.in)
		if tc.addr == "" {
			if err == nil {
				t.Errorf("bind %q accepted (%+v), want error", tc.in, b)
			}
			continue
		}
		if err != nil {
			t.Errorf("bind %q: %v", tc.in, err)
			continue
		}
		if b.Addr != tc.addr || b.Port != tc.port {
			t.Errorf("bind %q -> %+v, want addr %q port %d", tc.in, b, tc.addr, tc.port)
		}
	}
	for _, bad := range []string{"noport", "1.2.3.4:notanumber", "1.2.3.4:80:90", "260.0.0.1:80", "1.2.3.4:99999", "*", "-bad:80", "host name:80"} {
		if _, err := parseBindStr(bad); err == nil {
			t.Errorf("bind %q accepted, want error", bad)
		}
	}
}

func TestTCPKeepAliveAndReusePortPlacement(t *testing.T) {
	src := `
defaults
  mode tcp
  option tcpkeepalive
  tcp-keepalive idle 20s intvl 4s cnt 5

listen l
  mode tcp
  bind 0.0.0.0:80
  tcp-keepalive idle 10s cnt 2
  server s 10.0.0.1:80
`
	cfg, err := NewParser(strings.NewReader(src)).Config()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	d := cfg.Defaults
	if d.TCPKeepAliveIdle != 20*time.Second || d.TCPKeepAliveIntvl != 4*time.Second || d.TCPKeepAliveCnt != 5 {
		t.Errorf("defaults keepalive = idle %v intvl %v cnt %d", d.TCPKeepAliveIdle, d.TCPKeepAliveIntvl, d.TCPKeepAliveCnt)
	}
	be := cfg.Backends[0]
	if be.TCPKeepAliveIdle != 10*time.Second || be.TCPKeepAliveCnt != 2 {
		t.Errorf("listen keepalive = idle %v cnt %d", be.TCPKeepAliveIdle, be.TCPKeepAliveCnt)
	}

	for _, bad := range []string{
		"backend b\n  re-use-port\n  server s 1.2.3.4:80\n",
		"frontend f\n  bind 0.0.0.0:80\n  re-use-port\n",
		"frontend f\n  bind 0.0.0.0:80\n  tcp-keepalive idle 5s\n",
		"listen l\n  bind 0.0.0.0:80\n  re-use-port\n",
	} {
		if _, err := NewParser(strings.NewReader(bad)).Config(); err == nil {
			t.Errorf("misplaced directive accepted: %q", bad)
		}
	}
}

func TestLogTargetKeepsCase(t *testing.T) {
	src := `
global
  log C:\Soft\lsh-utproxy\logs\lsh-utproxy.log format json level debug
  maxconn 4000
`
	cfg, err := NewParser(strings.NewReader(src)).Config()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := `C:\Soft\lsh-utproxy\logs\lsh-utproxy.log`
	if cfg.Global.LogTarget != want {
		t.Errorf("log target = %q, want %q", cfg.Global.LogTarget, want)
	}
	if cfg.Global.LogFormat != "json" || cfg.Global.LogLevel != "debug" {
		t.Errorf("log format/level = %q / %q", cfg.Global.LogFormat, cfg.Global.LogLevel)
	}
	if cfg.Global.MaxConn != 4000 {
		t.Errorf("maxconn after a log line with a path = %d, want 4000", cfg.Global.MaxConn)
	}
}

// Access rules are read in file order, one rule per CIDR, and the order must
// survive parsing because the first matching rule wins.
func TestAccessRuleOrderInFrontend(t *testing.T) {
	src := `
frontend fe
  mode tcp
  bind 0.0.0.0:8080
  default_backend be
  allow all
  drop 173.24.5.0/24
  allow 192.168.1.0/24
  deny 10.12.13.0/24
  allow 10.20.0.0/16
  deny all
`
	cfg, err := NewParser(strings.NewReader(src)).Config()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fe := cfg.Frontends[0]
	want := [][2]string{
		{"allow", "all"},
		{"drop", "173.24.5.0/24"},
		{"allow", "192.168.1.0/24"},
		{"deny", "10.12.13.0/24"},
		{"allow", "10.20.0.0/16"},
		{"deny", "all"},
	}
	if len(fe.AccessRules) != len(want) {
		t.Fatalf("access rules = %d, want %d", len(fe.AccessRules), len(want))
	}
	for i, w := range want {
		r := fe.AccessRules[i]
		if r.Action != w[0] || r.Addr != w[1] {
			t.Errorf("rule %d = %s %s, want %s %s", i, r.Action, r.Addr, w[0], w[1])
		}
		if r.Line < 6 || r.Line > 11 {
			t.Errorf("rule %d line = %d, want the rule's own line", i, r.Line)
		}
	}
}

// A listen block is a frontend and a backend at once: the rules belong to the
// frontend half, and the block keeps working as before.
func TestAccessRulesInListen(t *testing.T) {
	src := `
listen rdp
  mode tcp
  bind 0.0.0.0:33390
  balance source
  deny 212.23.1.0/24
  allow all
  server rdp1 10.9.9.3:3389 check inter 10s
`
	cfg, err := NewParser(strings.NewReader(src)).Config()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fe := cfg.Frontends[0]
	be := cfg.Backends[0]
	if len(fe.AccessRules) != 2 || fe.AccessRules[0].Action != "deny" || fe.AccessRules[1].Addr != "all" {
		t.Errorf("listen access rules = %+v", fe.AccessRules)
	}
	if be.Name != "rdp" || len(be.Servers) != 1 || be.Servers[0].Name != "rdp1" {
		t.Errorf("listen backend broken: %+v", be)
	}
	if be.Balance != "source" {
		t.Errorf("balance lost: %q", be.Balance)
	}
}

// Several addresses on one line expand to that many rules, in the order
// written, all with the same action.
func TestAccessRulesSeveralAddressesOnOneLine(t *testing.T) {
	src := `
frontend f
  bind 0.0.0.0:80
  deny 10.1.0.0/16 10.2.0.0/16 2001:db8::/32
  allow all
`
	cfg, err := NewParser(strings.NewReader(src)).Config()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fe := cfg.Frontends[0]
	if len(fe.AccessRules) != 4 {
		t.Fatalf("rules = %d, want 4", len(fe.AccessRules))
	}
	for i, addr := range []string{"10.1.0.0/16", "10.2.0.0/16", "2001:db8::/32", "all"} {
		if fe.AccessRules[i].Addr != addr {
			t.Errorf("rule %d = %q, want %q", i, fe.AccessRules[i].Addr, addr)
		}
	}
	for i := 0; i < 3; i++ {
		if fe.AccessRules[i].Action != "deny" {
			t.Errorf("rule %d action = %q, want deny", i, fe.AccessRules[i].Action)
		}
	}
}

// Case-insensitive keywords, comment, quoting and blank lines must not shift
// the rules.
func TestAccessRulesKeywordsAreCaseInsensitive(t *testing.T) {
	src := `
frontend f
  bind 0.0.0.0:80
  DENY 10.12.13.0/24   # block the office
  Allow localhost
  deny all
`
	cfg, err := NewParser(strings.NewReader(src)).Config()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fe := cfg.Frontends[0]
	if len(fe.AccessRules) != 3 {
		t.Fatalf("rules = %d, want 3: %+v", len(fe.AccessRules), fe.AccessRules)
	}
	if fe.AccessRules[0].Action != "deny" || fe.AccessRules[1].Action != "allow" {
		t.Errorf("keyword case lost: %+v", fe.AccessRules)
	}
	if len(fe.Bind) != 1 || fe.Bind[0].Port != 80 {
		t.Errorf("bind broken: %+v", fe.Bind)
	}
}

func TestAccessRulesRejectBadInput(t *testing.T) {
	for _, bad := range []string{
		"frontend f\n  bind 0.0.0.0:80\n  deny\n",
		"frontend f\n  bind 0.0.0.0:80\n  deny 10.0.0.0/33\n",
		"frontend f\n  bind 0.0.0.0:80\n  allow notanetwork\n",
		"frontend f\n  bind 0.0.0.0:80\n  drop 260.1.1.1\n",
		"listen l\n  bind 0.0.0.0:80\n  server s 10.0.0.1:80\n  deny 10.0.0.0/99\n",
		// allow/deny/drop only make sense where traffic is accepted.
		"backend b\n  deny 10.0.0.0/8\n  server s 1.2.3.4:80\n",
		"defaults\n  deny all\n",
		"global\n  deny all\n",
	} {
		if _, err := NewParser(strings.NewReader(bad)).Config(); err == nil {
			t.Errorf("accepted, want an error: %q", bad)
		}
	}
}

// A block of rules must not swallow the directives around it.
func TestAccessRulesDoNotSwallowOtherDirectives(t *testing.T) {
	src := `
frontend f
  bind 0.0.0.0:8080
  maxconn 100
  deny 10.0.0.0/8
  timeout client 20s
  default_backend be
  timeout tunnel 3600s
`
	cfg, err := NewParser(strings.NewReader(src)).Config()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fe := cfg.Frontends[0]
	if fe.MaxConn != 100 {
		t.Errorf("maxconn = %d, want 100", fe.MaxConn)
	}
	if fe.TimeoutClient != 20*time.Second {
		t.Errorf("timeout client = %v", fe.TimeoutClient)
	}
	if fe.DefaultBackend != "be" {
		t.Errorf("default_backend = %q", fe.DefaultBackend)
	}
	if fe.TimeoutTunnel != 3600*time.Second {
		t.Errorf("timeout tunnel = %v", fe.TimeoutTunnel)
	}
	if len(fe.AccessRules) != 1 {
		t.Errorf("rules = %d, want 1", len(fe.AccessRules))
	}
}
