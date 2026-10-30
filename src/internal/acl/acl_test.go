package acl

import (
	"net"
	"testing"

	"lsh-utproxy/internal/config"
)

// rules builds an ordered rule list the way the parser produces it.
func rules(pairs ...string) []config.AccessRule {
	out := make([]config.AccessRule, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, config.AccessRule{Action: pairs[i], Addr: pairs[i+1], Line: i/2 + 1})
	}
	return out
}

func TestFirstMatchingRuleWinsInOrder(t *testing.T) {
	// The user's example: rules are evaluated top to bottom and the first
	// match ends the walk, so "allow all" on the first line makes the rest of
	// the block dead text.
	l, err := New(rules("allow", "all", "drop", "173.24.5.0/24", "allow", "192.168.1.0/24",
		"deny", "10.12.13.0/24", "allow", "10.20.0.0/16", "deny", "all"))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if l.Len() != 6 {
		t.Fatalf("compiled %d rules, want 6", l.Len())
	}
	for _, ip := range []string{"10.12.13.5", "8.8.8.8", "173.24.5.9", "::1"} {
		if a, rule := l.Decide(net.ParseIP(ip)); a != ActionAllow {
			t.Errorf("%s -> %s (%q), want allow from the first rule", ip, a, rule)
		}
	}
}

func TestOrderedBlock(t *testing.T) {
	// The same block without "allow all", so every rule is reachable.
	l, err := New(rules("drop", "173.24.5.0/24", "allow", "192.168.1.0/24",
		"deny", "10.12.13.0/24", "allow", "10.20.0.0/16", "deny", "all"))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	cases := []struct {
		ip   string
		want Action
	}{
		{"173.24.5.99", ActionDrop},
		{"192.168.1.7", ActionAllow},
		{"10.12.13.4", ActionDeny},
		// 10.12.13.0/24 is not a supernet of 10.20.0.0/16, so this one is
		// matched by the allow before it reaches "deny all".
		{"10.20.5.6", ActionAllow},
		{"10.12.14.1", ActionDeny},
		{"8.8.8.8", ActionDeny},
		{"2001:db8::5", ActionDeny},
		{"::1", ActionDeny},
	}
	for _, c := range cases {
		a, rule := l.Decide(net.ParseIP(c.ip))
		if a != c.want {
			t.Errorf("%s -> %s (%q), want %s", c.ip, a, rule, c.want)
		}
	}
}

func TestDenyBeforeAllowSameSubnet(t *testing.T) {
	l, err := New(rules("deny", "127.0.0.0/8", "allow", "all"))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	a, rule := l.Decide(net.ParseIP("127.0.0.1"))
	if a != ActionDeny {
		t.Fatalf("127.0.0.1 -> %s, want deny (order matters, first match wins)", a)
	}
	if rule != "deny 127.0.0.0/8" {
		t.Errorf("rule text = %q", rule)
	}
	a, _ = l.Decide(net.ParseIP("10.0.0.1"))
	if a != ActionAllow {
		t.Errorf("10.0.0.1 -> %s, want allow", a)
	}
}

func TestAddressForms(t *testing.T) {
	cases := []struct {
		addr    string
		in      string
		out     string
		wantIn  Action
		wantOut Action
	}{
		{"173.24.5.136", "173.24.5.136", "173.24.5.137", ActionDeny, ActionAllow},
		{"173.24.5.136/24", "173.24.5.200", "173.24.6.1", ActionDeny, ActionAllow},
		{"2001:db8::/32", "2001:db8:beef::1", "2001:db9::1", ActionDeny, ActionAllow},
		{"fe80::1", "fe80::1", "fe80::2", ActionDeny, ActionAllow},
		{"localhost", "127.0.0.1", "10.0.0.1", ActionDeny, ActionAllow},
		{"localhost", "::1", "2001:db8::1", ActionDeny, ActionAllow},
		{"all", "8.8.8.8", "", ActionDeny, ActionAllow},
	}
	for _, c := range cases {
		l, err := New(rules("deny", c.addr, "allow", "all"))
		if err != nil {
			t.Fatalf("%s: compile: %v", c.addr, err)
		}
		a, rule := l.Decide(net.ParseIP(c.in))
		if a != c.wantIn {
			t.Errorf("deny %s: %s -> %s (%q), want %s", c.addr, c.in, a, rule, c.wantIn)
		}
		if c.out != "" {
			if a2, _ := l.Decide(net.ParseIP(c.out)); a2 != c.wantOut {
				t.Errorf("deny %s: %s -> %s, want %s", c.addr, c.out, a2, c.wantOut)
			}
		}
	}
}

func TestIPv4RuleDoesNotMatchIPv6(t *testing.T) {
	l, err := New(rules("deny", "127.0.0.0/8"))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if l.Blocked(net.ParseIP("::1")) {
		t.Errorf("an IPv4 rule matched ::1")
	}
	if !l.Blocked(net.ParseIP("127.0.0.1")) {
		t.Errorf("127.0.0.1 not blocked")
	}
	// Go gives IPv4 addresses in the IPv4-in-IPv6 form on dual-stack sockets.
	if !l.Blocked(net.ParseIP("::ffff:127.0.0.1")) {
		t.Errorf("::ffff:127.0.0.1 not matched by 127.0.0.0/8")
	}
}

func TestNoRulesAllowEverything(t *testing.T) {
	l, err := New(nil)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if l.Len() != 0 || l.Blocked(net.ParseIP("1.2.3.4")) {
		t.Errorf("empty list blocked traffic")
	}
	var nilList *List
	if a, _ := nilList.Decide(net.ParseIP("1.2.3.4")); a != ActionAllow {
		t.Errorf("nil list did not allow")
	}
}

func TestBadAddressRejectedAtCompile(t *testing.T) {
	for _, bad := range []string{"notanip", "10.0.0.0/33", "10.0.0.0/abc", "260.1.1.1", "10.0.0.0.0/8", "fe80::1%eth0"} {
		if _, err := New(rules("deny", bad)); err == nil {
			t.Errorf("addr %q accepted, want error", bad)
		}
	}
	if _, err := New([]config.AccessRule{{Action: "permit", Addr: "all"}}); err == nil {
		t.Errorf("unknown action accepted, want error")
	}
}

func TestRuleLogIsRateLimited(t *testing.T) {
	l, err := New(rules("deny", "all"))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !l.ShouldLog() {
		t.Fatalf("the first blocked line must be logged")
	}
	if l.ShouldLog() {
		t.Errorf("the second line within the window was logged")
	}
}
