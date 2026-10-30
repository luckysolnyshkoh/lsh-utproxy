package acl

import (
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"lsh-utproxy/internal/config"
)

// Action is what a matching rule does with the traffic.
type Action int

const (
	// ActionAllow lets the connection or datagram through.
	ActionAllow Action = iota
	// ActionDeny refuses it: a TCP client gets a connection reset (the
	// iptables REJECT behaviour), a UDP datagram is not answered.
	ActionDeny
	// ActionDrop discards it silently: the TCP socket is closed without
	// telling anyone (the iptables DROP behaviour), a UDP datagram is not
	// answered.
	ActionDrop
)

func (a Action) String() string {
	switch a {
	case ActionAllow:
		return "allow"
	case ActionDeny:
		return "deny"
	case ActionDrop:
		return "drop"
	}
	return "allow"
}

type entry struct {
	action Action
	nets   []*net.IPNet // empty = "all": every source address matches
	text   string       // the directive as written, e.g. "deny 10.12.13.0/24"
}

// List holds the access rules of one frontend in the order they appear in the
// config. The first rule that matches the source address decides; the rules
// below it are not evaluated for that address.
type List struct {
	entries []entry
	lastLog atomic.Int64 // unix nanos of the last "blocked" log line
}

// New compiles the config rules of one section. Rule order is preserved, so
// "allow 10.0.0.0/8" before "deny all" keeps the 10/8 traffic in.
func New(rules []config.AccessRule) (*List, error) {
	l := &List{}
	for _, r := range rules {
		var a Action
		switch strings.ToLower(r.Action) {
		case "allow":
			a = ActionAllow
		case "deny":
			a = ActionDeny
		case "drop":
			a = ActionDrop
		default:
			return nil, fmt.Errorf("unknown access action %q", r.Action)
		}
		nets, err := parseAddr(r.Addr)
		if err != nil {
			return nil, fmt.Errorf("line %d: %s %s: %w", r.Line, r.Action, r.Addr, err)
		}
		l.entries = append(l.entries, entry{action: a, nets: nets, text: r.Action + " " + r.Addr})
	}
	return l, nil
}

// Decide returns the action of the first rule matching ip. With no rule, or no
// matching rule, the traffic is allowed: like nginx, a list that nothing
// matched lets the address through.
func (l *List) Decide(ip net.IP) (Action, string) {
	if l == nil || len(l.entries) == 0 {
		return ActionAllow, ""
	}
	for _, e := range l.entries {
		if e.matches(ip) {
			return e.action, e.text
		}
	}
	return ActionAllow, ""
}

// Blocked reports whether ip must not be served.
func (l *List) Blocked(ip net.IP) bool {
	a, _ := l.Decide(ip)
	return a != ActionAllow
}

func (e entry) matches(ip net.IP) bool {
	if len(e.nets) == 0 {
		return true // "all"
	}
	for _, n := range e.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Len is the number of compiled rules; 0 means "no filtering".
func (l *List) Len() int {
	if l == nil {
		return 0
	}
	return len(l.entries)
}

// ShouldLog limits the "blocked by rule" line to one per five seconds per
// frontend, so a scanner hitting a deny-all rule cannot fill the log.
func (l *List) ShouldLog() bool {
	if l == nil {
		return false
	}
	now := time.Now().UnixNano()
	last := l.lastLog.Load()
	if now-last < int64(5*time.Second) {
		return false
	}
	return l.lastLog.CompareAndSwap(last, now)
}

// parseAddr turns one rule argument into the networks it covers.
func parseAddr(s string) ([]*net.IPNet, error) {
	switch strings.ToLower(s) {
	case "all", "any", "*":
		return nil, nil
	case "localhost", "local", "loopback":
		return []*net.IPNet{
			{IP: net.IPv4(127, 0, 0, 0), Mask: net.CIDRMask(8, 32)},
			{IP: net.IPv6loopback, Mask: net.CIDRMask(128, 128)},
		}, nil
	}
	if strings.Contains(s, "/") {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return nil, fmt.Errorf("%q is not a CIDR network", s)
		}
		return []*net.IPNet{n}, nil
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return nil, fmt.Errorf("%q is not an IP address", s)
	}
	if ip.To4() != nil {
		return []*net.IPNet{&net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)}}, nil
	}
	return []*net.IPNet{&net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}}, nil
}

// ClientIP extracts the source address from a connection or datagram address.
func ClientIP(a net.Addr) net.IP {
	switch v := a.(type) {
	case *net.TCPAddr:
		return v.IP
	case *net.UDPAddr:
		return v.IP
	}
	if a == nil {
		return nil
	}
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return nil
	}
	return net.ParseIP(host)
}
