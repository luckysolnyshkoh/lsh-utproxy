package config

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

type Parser struct {
	lexer *Lexer
	cur   Token
}

func NewParser(r io.Reader) *Parser {
	l := NewLexer(r)
	p := &Parser{lexer: l}
	p.next()
	return p
}

func (p *Parser) next() {
	p.cur = p.lexer.Next()
}

func (p *Parser) Config() (*Config, error) {
	cfg := &Config{}
	cfg.Defaults = Defaults{
		Mode:           "tcp",
		Balance:        "roundrobin",
		TimeoutConnect: 5 * time.Second,
		TimeoutClient:  30 * time.Second,
		TimeoutServer:  30 * time.Second,
		// TimeoutTunnel and TimeoutIdle stay zero: "not configured". The TCP
		// copy engine resolves them in HAProxy order and falls back to one
		// hour, so an idle long-lived tunnel is not killed by a built-in
		// 60s default.
		TimeoutTunnel:      0,
		TimeoutIdle:        0,
		TimeoutCheck:       2 * time.Second,
		MaxConn:            0,
		MaxConnFrontend:    0,
		MaxConnBackend:     0,
		OptionTCPKeepAlive: true,
		TCPKeepAliveIdle:   30 * time.Second,
		TCPKeepAliveIntvl:  5 * time.Second,
		TCPKeepAliveCnt:    3,
		BufferSizeRead:     8 * 1024,
		BufferSizeWrite:    8 * 1024,
		UDPSessionTTL:      30 * time.Second,
		UDPMaxSessions:     10000,
		UDPBufferSize:      64 * 1024,
		ReUsePort:          false,
	}
	cfg.Global = Global{
		LogLevel:      "info",
		LogFormat:     "json",
		MaxConn:       0,
		MaxProcs:      0,
		StatsTimeout:  10 * time.Second,
		GraceShutdown: 10 * time.Second,
	}

	for p.cur.Type != TokenEOF {
		if p.cur.Type != TokenWord {
			p.next()
			continue
		}
		// Only treat non-indented words as top-level section headers.
		if p.cur.Indented {
			// stray indented token at top level; skip it
			p.next()
			continue
		}
		kw := strings.ToLower(p.cur.Value)
		p.next()
		switch kw {
		case "global":
			if err := p.parseGlobal(cfg); err != nil {
				return nil, err
			}
		case "defaults":
			if err := p.parseDefaults(cfg); err != nil {
				return nil, err
			}
		case "frontend":
			fe, err := p.parseFrontend()
			if err != nil {
				return nil, err
			}
			cfg.Frontends = append(cfg.Frontends, fe)
		case "backend":
			be, err := p.parseBackend()
			if err != nil {
				return nil, err
			}
			cfg.Backends = append(cfg.Backends, be)
		case "listen":
			lst, err := p.parseListen()
			if err != nil {
				return nil, err
			}
			cfg.Frontends = append(cfg.Frontends, lst.Frontend)
			cfg.Backends = append(cfg.Backends, lst.Backend)
		default:
		}
	}
	return cfg, nil
}

func (p *Parser) inSection() bool {
	return p.cur.Type == TokenWord && p.cur.Indented
}

// sameLine reports whether the current token is an argument that continues the
// directive being parsed: an indented word that does not start a new line.
// Without it, directives whose arguments are read in a loop (server options,
// log options) swallow the next line and silently drop backends.
func (p *Parser) sameLine() bool {
	return p.cur.Type == TokenWord && p.cur.Indented && !p.cur.FirstInLine
}

// skipLine drops the remaining arguments of an unknown directive.
func (p *Parser) skipLine() {
	for p.sameLine() {
		p.next()
	}
}

type listenPair struct {
	Frontend Frontend
	Backend  Backend
}

func (p *Parser) parseListen() (listenPair, error) {
	var lp listenPair
	if p.cur.Type != TokenWord || p.cur.Indented {
		return lp, fmt.Errorf("listen: expected name")
	}
	name := unquote(p.cur.Value)
	p.next()
	lp.Frontend = Frontend{Name: name, Bind: []Bind{}, Options: map[string]string{}}
	lp.Backend = Backend{Name: name, Servers: []Server{}, Options: map[string]string{}}
	for p.inSection() {
		kw := strings.ToLower(p.cur.Value)
		line := p.cur.Line
		p.next()
		switch kw {
		case "allow", "deny", "drop":
			rules, err := p.parseAccessLine(kw, line)
			if err != nil {
				return lp, err
			}
			lp.Frontend.AccessRules = append(lp.Frontend.AccessRules, rules...)
		case "mode":
			if p.sameLine() {
				m := strings.ToLower(unquote(p.cur.Value))
				p.next()
				lp.Frontend.Mode = m
				lp.Backend.Mode = m
			}
		case "bind":
			if p.sameLine() {
				bs := unquote(p.cur.Value)
				p.next()
				b, err := parseBindStr(bs)
				if err != nil {
					return lp, err
				}
				lp.Frontend.Bind = append(lp.Frontend.Bind, b)
			}
		case "maxconn":
			if v, ok := p.parseIntSection(); ok {
				lp.Frontend.MaxConn = v
				lp.Backend.MaxConn = v
			}
		case "timeout":
			if p.sameLine() {
				tk := strings.ToLower(unquote(p.cur.Value))
				p.next()
				if d, ok := p.parseDurationSection(); ok {
					switch tk {
					case "connect":
						lp.Backend.TimeoutConnect = d
					case "client":
						lp.Frontend.TimeoutClient = d
					case "server":
						lp.Backend.TimeoutServer = d
					case "tunnel":
						lp.Frontend.TimeoutTunnel = d
						lp.Backend.TimeoutTunnel = d
					case "idle":
						lp.Frontend.TimeoutIdle = d
						lp.Backend.TimeoutIdle = d
					case "check":
						lp.Backend.TimeoutCheck = d
					}
				}
			}
		case "balance":
			if p.sameLine() {
				lp.Backend.Balance = strings.ToLower(unquote(p.cur.Value))
				p.next()
			}
		case "server":
			s, err := p.parseServerLineSection()
			if err != nil {
				return lp, err
			}
			lp.Backend.Servers = append(lp.Backend.Servers, s)
		case "option":
			if p.sameLine() {
				okw := strings.ToLower(unquote(p.cur.Value))
				p.next()
				switch okw {
				case "tcpka", "tcpkeepalive":
					lp.Frontend.OptionTCPKeepAlive = true
					lp.Backend.OptionTCPKeepAlive = true
				}
			}
		case "udp-session-ttl":
			if d, ok := p.parseDurationSection(); ok {
				lp.Backend.UDPSessionTTL = d
			}
		case "udp-max-sessions":
			if v, ok := p.parseIntSection(); ok {
				lp.Backend.UDPMaxSessions = v
			}
		case "udp-buffer-size":
			if v, ok := p.parseIntSection(); ok {
				lp.Backend.UDPBufferSize = v
			}
		case "re-use-port", "reuseport":
			return lp, fmt.Errorf("re-use-port is a process-wide switch: put it in the defaults section")
		case "tcp-keepalive":
			p.keepAliveArgs(&lp.Backend.TCPKeepAliveIdle, &lp.Backend.TCPKeepAliveIntvl, &lp.Backend.TCPKeepAliveCnt)
		default:
			// skip unknown keyword's argument if present on same line
			p.skipLine()
		}
	}
	lp.Frontend.DefaultBackend = name
	return lp, nil
}

// keepAliveArgs parses "tcp-keepalive [idle <t>] [intvl <t>] [cnt <n>]" that
// appears on one line in defaults, backend or listen sections.
func (p *Parser) keepAliveArgs(idle *time.Duration, intvl *time.Duration, cnt *int) {
	for p.sameLine() {
		k := strings.ToLower(unquote(p.cur.Value))
		p.next()
		switch k {
		case "idle":
			d, ok := p.parseDurationSection()
			if !ok {
				return
			}
			*idle = d
		case "intvl", "interval":
			d, ok := p.parseDurationSection()
			if !ok {
				return
			}
			*intvl = d
		case "cnt", "count":
			v, ok := p.parseIntSection()
			if !ok {
				return
			}
			*cnt = v
		default:
			return
		}
	}
}

func (p *Parser) parseGlobal(cfg *Config) error {
	for p.inSection() {
		kw := strings.ToLower(p.cur.Value)
		line := p.cur.Line
		p.next()
		switch kw {
		case "allow", "deny", "drop":
			return accessPlacementErr("global", kw, line)
		case "log":
			// "log <target> [format <fmt>] [level <lvl>]" is one line. The loop
			// is line-scoped so the next directive is not swallowed. The target
			// keeps its case: on Windows it is usually a path.
			if p.sameLine() {
				cfg.Global.LogTarget = unquote(p.cur.Value)
				p.next()
			}
			for p.sameLine() {
				lk := strings.ToLower(unquote(p.cur.Value))
				if lk != "format" && lk != "level" {
					break
				}
				p.next()
				if !p.sameLine() {
					break
				}
				v := strings.ToLower(unquote(p.cur.Value))
				if lk == "format" {
					cfg.Global.LogFormat = v
				} else {
					cfg.Global.LogLevel = v
				}
				p.next()
			}
		case "maxconn":
			if v, ok := p.parseIntSection(); ok {
				cfg.Global.MaxConn = v
			}
		case "nbproc", "maxprocs":
			if v, ok := p.parseIntSection(); ok {
				if v < 0 {
					v = 0
				}
				cfg.Global.MaxProcs = v
			}
		case "stats":
			if p.sameLine() {
				sk := strings.ToLower(unquote(p.cur.Value))
				p.next()
				if sk == "socket" && p.sameLine() {
					cfg.Global.StatsSocket = unquote(p.cur.Value)
					p.next()
				} else if sk == "timeout" {
					if d, ok := p.parseDurationSection(); ok {
						cfg.Global.StatsTimeout = d
					}
				}
			}
		case "grace":
			if p.sameLine() {
				gk := strings.ToLower(unquote(p.cur.Value))
				p.next()
				if gk == "shutdown" {
					if d, ok := p.parseDurationSection(); ok {
						cfg.Global.GraceShutdown = d
					}
				}
			}
		case "chroot":
			if p.sameLine() {
				cfg.Global.Chroot = unquote(p.cur.Value)
				p.next()
			}
		case "user":
			if p.sameLine() {
				cfg.Global.User = unquote(p.cur.Value)
				p.next()
			}
		case "group":
			if p.sameLine() {
				cfg.Global.Group = unquote(p.cur.Value)
				p.next()
			}
		case "daemon":
			cfg.Global.Daemon = true
		default:
			p.skipLine()
		}
	}
	return nil
}

func (p *Parser) parseDefaults(cfg *Config) error {
	for p.inSection() {
		kw := strings.ToLower(p.cur.Value)
		line := p.cur.Line
		p.next()
		switch kw {
		case "allow", "deny", "drop":
			return accessPlacementErr("defaults", kw, line)
		case "mode":
			if p.sameLine() {
				cfg.Defaults.Mode = strings.ToLower(unquote(p.cur.Value))
				p.next()
			}
		case "balance":
			if p.sameLine() {
				cfg.Defaults.Balance = strings.ToLower(unquote(p.cur.Value))
				p.next()
			}
		case "timeout":
			if p.sameLine() {
				tk := strings.ToLower(unquote(p.cur.Value))
				p.next()
				if d, ok := p.parseDurationSection(); ok {
					switch tk {
					case "connect":
						cfg.Defaults.TimeoutConnect = d
					case "client":
						cfg.Defaults.TimeoutClient = d
					case "server":
						cfg.Defaults.TimeoutServer = d
					case "tunnel":
						cfg.Defaults.TimeoutTunnel = d
					case "idle":
						cfg.Defaults.TimeoutIdle = d
					case "check":
						cfg.Defaults.TimeoutCheck = d
					}
				}
			}
		case "maxconn":
			if v, ok := p.parseIntSection(); ok {
				cfg.Defaults.MaxConn = v
			}
		case "option":
			if p.sameLine() {
				okw := strings.ToLower(unquote(p.cur.Value))
				p.next()
				switch okw {
				case "tcpka", "tcpkeepalive":
					cfg.Defaults.OptionTCPKeepAlive = true
				}
			}
		case "re-use-port", "reuseport":
			cfg.Defaults.ReUsePort = true
		case "tcp-keepalive":
			p.keepAliveArgs(&cfg.Defaults.TCPKeepAliveIdle, &cfg.Defaults.TCPKeepAliveIntvl, &cfg.Defaults.TCPKeepAliveCnt)
		case "udp-max-sessions":
			if v, ok := p.parseIntSection(); ok {
				cfg.Defaults.UDPMaxSessions = v
			}
		case "udp-session-ttl":
			if d, ok := p.parseDurationSection(); ok {
				cfg.Defaults.UDPSessionTTL = d
			}
		case "udp-buffer-size":
			if v, ok := p.parseIntSection(); ok {
				cfg.Defaults.UDPBufferSize = v
			}
		default:
			p.skipLine()
		}
	}
	return nil
}

func (p *Parser) parseFrontend() (Frontend, error) {
	var fe Frontend
	fe.Options = map[string]string{}
	fe.Bind = []Bind{}
	if p.cur.Type != TokenWord || p.cur.Indented {
		return fe, fmt.Errorf("frontend: expected name")
	}
	fe.Name = unquote(p.cur.Value)
	p.next()
	for p.inSection() {
		kw := strings.ToLower(p.cur.Value)
		line := p.cur.Line
		p.next()
		switch kw {
		case "allow", "deny", "drop":
			rules, err := p.parseAccessLine(kw, line)
			if err != nil {
				return fe, err
			}
			fe.AccessRules = append(fe.AccessRules, rules...)
		case "mode":
			if p.sameLine() {
				fe.Mode = strings.ToLower(unquote(p.cur.Value))
				p.next()
			}
		case "bind":
			if p.sameLine() {
				bs := unquote(p.cur.Value)
				p.next()
				b, err := parseBindStr(bs)
				if err != nil {
					return fe, err
				}
				fe.Bind = append(fe.Bind, b)
			}
		case "default_backend":
			if p.sameLine() {
				fe.DefaultBackend = unquote(p.cur.Value)
				p.next()
			}
		case "maxconn":
			if v, ok := p.parseIntSection(); ok {
				fe.MaxConn = v
			}
		case "timeout":
			if p.sameLine() {
				tk := strings.ToLower(unquote(p.cur.Value))
				p.next()
				if d, ok := p.parseDurationSection(); ok {
					switch tk {
					case "client":
						fe.TimeoutClient = d
					case "tunnel":
						fe.TimeoutTunnel = d
					case "idle":
						fe.TimeoutIdle = d
					}
				}
			}
		case "option":
			if p.sameLine() {
				okw := strings.ToLower(unquote(p.cur.Value))
				p.next()
				switch okw {
				case "tcpka", "tcpkeepalive":
					fe.OptionTCPKeepAlive = true
				}
			}
		case "acl":
			if p.sameLine() {
				an := unquote(p.cur.Value)
				p.next()
				if p.sameLine() {
					cr := strings.ToLower(unquote(p.cur.Value))
					p.next()
					var val string
					if p.sameLine() {
						val = unquote(p.cur.Value)
						p.next()
					}
					fe.ACLs = append(fe.ACLs, ACL{Name: an, Criterion: cr, Value: val})
				}
			}
		case "use_backend":
			if p.sameLine() {
				bn := unquote(p.cur.Value)
				p.next()
				var cond string
				if p.sameLine() {
					c1 := strings.ToLower(unquote(p.cur.Value))
					if c1 == "if" || c1 == "unless" {
						p.next()
						if p.sameLine() {
							cond = c1 + " " + unquote(p.cur.Value)
							p.next()
						} else {
							cond = c1
						}
					} else {
						cond = c1
						p.next()
					}
				}
				fe.UseBackendRules = append(fe.UseBackendRules, UseBackend{Backend: bn, Condition: cond})
			}
		case "re-use-port", "reuseport":
			return fe, fmt.Errorf("re-use-port is a process-wide switch: put it in the defaults section")
		case "tcp-keepalive":
			return fe, fmt.Errorf("tcp-keepalive timings belong to defaults, backend or listen, not a frontend")
		default:
			p.skipLine()
		}
	}
	return fe, nil
}

func (p *Parser) parseBackend() (Backend, error) {
	var be Backend
	be.Options = map[string]string{}
	be.Servers = []Server{}
	if p.cur.Type != TokenWord || p.cur.Indented {
		return be, fmt.Errorf("backend: expected name")
	}
	be.Name = unquote(p.cur.Value)
	p.next()
	for p.inSection() {
		kw := strings.ToLower(p.cur.Value)
		line := p.cur.Line
		p.next()
		switch kw {
		case "allow", "deny", "drop":
			return be, accessPlacementErr("backend", kw, line)
		case "mode":
			if p.sameLine() {
				be.Mode = strings.ToLower(unquote(p.cur.Value))
				p.next()
			}
		case "balance":
			if p.sameLine() {
				be.Balance = strings.ToLower(unquote(p.cur.Value))
				p.next()
			}
		case "server":
			s, err := p.parseServerLineSection()
			if err != nil {
				return be, err
			}
			be.Servers = append(be.Servers, s)
		case "maxconn":
			if v, ok := p.parseIntSection(); ok {
				be.MaxConn = v
			}
		case "timeout":
			if p.sameLine() {
				tk := strings.ToLower(unquote(p.cur.Value))
				p.next()
				if d, ok := p.parseDurationSection(); ok {
					switch tk {
					case "connect":
						be.TimeoutConnect = d
					case "server":
						be.TimeoutServer = d
					case "tunnel":
						be.TimeoutTunnel = d
					case "idle":
						be.TimeoutIdle = d
					case "check":
						be.TimeoutCheck = d
					}
				}
			}
		case "option":
			if p.sameLine() {
				okw := strings.ToLower(unquote(p.cur.Value))
				p.next()
				switch okw {
				case "tcpka", "tcpkeepalive":
					be.OptionTCPKeepAlive = true
				}
			}
		case "re-use-port", "reuseport":
			return be, fmt.Errorf("re-use-port is a process-wide switch: put it in the defaults section")
		case "tcp-keepalive":
			p.keepAliveArgs(&be.TCPKeepAliveIdle, &be.TCPKeepAliveIntvl, &be.TCPKeepAliveCnt)
		case "udp-session-ttl":
			if d, ok := p.parseDurationSection(); ok {
				be.UDPSessionTTL = d
			}
		case "udp-max-sessions":
			if v, ok := p.parseIntSection(); ok {
				be.UDPMaxSessions = v
			}
		case "udp-buffer-size":
			if v, ok := p.parseIntSection(); ok {
				be.UDPBufferSize = v
			}
		default:
			p.skipLine()
		}
	}
	return be, nil
}

// parseAccessLine reads one "allow|deny|drop <addr>" line inside a frontend or
// listen section. One rule per address, in written order; several addresses on
// one line expand to several rules with the same action. The list is kept in
// file order because the first matching rule wins.
func (p *Parser) parseAccessLine(action string, line int) ([]AccessRule, error) {
	var out []AccessRule
	for p.sameLine() {
		addr := unquote(p.cur.Value)
		p.next()
		if !validAccessAddr(addr) {
			return nil, fmt.Errorf("line %d: %s: %q is not a CIDR network, an IP address, \"all\" or \"localhost\"", line, action, addr)
		}
		out = append(out, AccessRule{Action: action, Addr: addr, Line: line})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("line %d: %s: expected an address (CIDR network, IP, all, localhost)", line, action)
	}
	return out, nil
}

// validAccessAddr accepts the forms access rules support: "all", "localhost",
// a CIDR network (IPv4 or IPv6) and a single address.
func validAccessAddr(s string) bool {
	switch strings.ToLower(s) {
	case "all", "any", "*", "localhost", "local", "loopback":
		return true
	}
	if strings.Contains(s, "/") {
		_, _, err := net.ParseCIDR(s)
		return err == nil
	}
	return net.ParseIP(s) != nil
}

// accessPlacementErr rejects allow/deny/drop outside a frontend or listen
// section: a rule silently ignored is worse than a startup error.
func accessPlacementErr(section, action string, line int) error {
	return fmt.Errorf("line %d: %s: access rules (allow/deny/drop) belong to a frontend or listen section, not to %s", line, action, section)
}

func (p *Parser) parseServerLineSection() (Server, error) {
	var s Server
	if p.cur.Type != TokenWord {
		return s, fmt.Errorf("server: expected name")
	}
	s.Name = unquote(p.cur.Value)
	p.next()
	if !p.sameLine() {
		return s, fmt.Errorf("server %s: expected addr", s.Name)
	}
	s.Addr = unquote(p.cur.Value)
	p.next()
	for p.sameLine() {
		param := strings.ToLower(unquote(p.cur.Value))
		p.next()
		switch param {
		case "check":
			s.Check = true
		case "backup":
			s.Backup = true
		case "weight":
			if v, ok := p.parseIntSection(); ok {
				if v < 0 {
					v = 0
				}
				if v > 256 {
					v = 256
				}
				s.Weight = v
			}
		case "maxconn":
			if v, ok := p.parseIntSection(); ok {
				s.MaxConn = v
			}
		case "inter":
			if d, ok := p.parseDurationSection(); ok {
				s.Inter = d
			}
		case "rise":
			if v, ok := p.parseIntSection(); ok {
				s.Rise = v
			}
		case "fall":
			if v, ok := p.parseIntSection(); ok {
				s.Fall = v
			}
		case "port":
			if v, ok := p.parseIntSection(); ok {
				s.Port = v
			}
		default:
		}
	}
	if s.Weight == 0 {
		s.Weight = 1
	}
	return s, nil
}

func (p *Parser) parseIntSection() (int, bool) {
	if !p.sameLine() {
		return 0, false
	}
	v, err := strconv.Atoi(unquote(p.cur.Value))
	if err != nil {
		return 0, false
	}
	p.next()
	return v, true
}

func (p *Parser) parseDurationSection() (time.Duration, bool) {
	if !p.sameLine() {
		return 0, false
	}
	s := unquote(p.cur.Value)
	p.next()
	d, err := parseDurationStr(s)
	if err != nil {
		return 0, false
	}
	return d, true
}

// parseBindStr accepts the HAProxy bind forms: "0.0.0.0:80", "*:80",
// "[::1]:8080", "::80" (all IPv6), ":8080" (all interfaces) and a host name
// with a port. The result is always a literal "host:port" ready for
// net.Listen / net.ResolveUDPAddr.
func parseBindStr(s string) (Bind, error) {
	var b Bind
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return b, fmt.Errorf("bind: empty address")
	}
	switch {
	case s == "*":
		return b, fmt.Errorf("bind %q: missing port, use *:<port>", s)
	case strings.HasPrefix(s, "*:"):
		s = "0.0.0.0:" + s[2:]
	case strings.HasPrefix(s, "::") && !strings.HasPrefix(s, "[::"):
		// "::80" style: any IPv6 address
		s = "[::]:" + strings.TrimPrefix(s, "::")
	}
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		if strings.Contains(err.Error(), "missing port") {
			return b, fmt.Errorf("bind %q: missing port, use host:port", s)
		}
		return b, fmt.Errorf("bind %q: %v", s, err)
	}
	if host == "" || host == "*" {
		host = "0.0.0.0"
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 65535 {
		return b, fmt.Errorf("bind %q: invalid port %q", s, portStr)
	}
	if !validBindHost(host) {
		return b, fmt.Errorf("bind %q: invalid host %q (want IP address, [IPv6] or host name)", s, host)
	}
	addr := host + ":" + portStr
	if strings.Contains(host, ":") {
		addr = "[" + host + "]:" + portStr
	}
	b.Addr = addr
	b.Port = port
	return b, nil
}

// validBindHost allows IP literals and RFC1123 host names.
func validBindHost(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	if net.ParseIP(h) != nil {
		return true
	}
	// Digits and dots only: it was meant to be an IP but did not parse as one.
	numeric := true
	for i := 0; i < len(h); i++ {
		c := h[i]
		isDigit := c >= '0' && c <= '9'
		if !isDigit && c != '.' {
			numeric = false
			break
		}
	}
	if numeric {
		return false
	}
	if strings.ContainsAny(h, "[]/%@ ") {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			alnum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
			if !alnum && c != '-' {
				return false
			}
			if c == '-' && (i == 0 || i == len(label)-1) {
				return false
			}
		}
	}
	return true
}

func parseDurationStr(s string) (time.Duration, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	mul := time.Second
	if strings.HasSuffix(s, "us") {
		mul = time.Microsecond
		s = strings.TrimSuffix(s, "us")
	} else if strings.HasSuffix(s, "ms") {
		mul = time.Millisecond
		s = strings.TrimSuffix(s, "ms")
	} else if strings.HasSuffix(s, "s") {
		mul = time.Second
		s = strings.TrimSuffix(s, "s")
	} else if strings.HasSuffix(s, "m") {
		mul = time.Minute
		s = strings.TrimSuffix(s, "m")
	} else if strings.HasSuffix(s, "h") {
		mul = time.Hour
		s = strings.TrimSuffix(s, "h")
	} else if strings.HasSuffix(s, "d") {
		mul = 24 * time.Hour
		s = strings.TrimSuffix(s, "d")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if v < 0 {
		v = 0
	}
	return time.Duration(v * float64(mul)), nil
}
