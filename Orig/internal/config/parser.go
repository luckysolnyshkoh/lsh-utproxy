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
		Mode:               "tcp",
		Balance:            "roundrobin",
		TimeoutConnect:     5 * time.Second,
		TimeoutClient:      30 * time.Second,
		TimeoutServer:      30 * time.Second,
		TimeoutTunnel:      1 * time.Hour,
		TimeoutIdle:        60 * time.Second,
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
		p.next()
		switch kw {
		case "mode":
			if p.inSection() {
				m := strings.ToLower(unquote(p.cur.Value))
				p.next()
				lp.Frontend.Mode = m
				lp.Backend.Mode = m
			}
		case "bind":
			if p.inSection() {
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
			if p.inSection() {
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
			if p.inSection() {
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
			if p.inSection() {
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
		default:
			// skip unknown keyword's argument if present on same line
			if p.inSection() {
				p.next()
			}
		}
	}
	lp.Frontend.DefaultBackend = name
	return lp, nil
}

func (p *Parser) parseGlobal(cfg *Config) error {
	for p.inSection() {
		kw := strings.ToLower(p.cur.Value)
		p.next()
		switch kw {
		case "log":
			if p.inSection() {
				p.next()
			}
			for p.inSection() {
				lk := strings.ToLower(unquote(p.cur.Value))
				if lk == "format" {
					p.next()
					if p.inSection() {
						cfg.Global.LogFormat = strings.ToLower(unquote(p.cur.Value))
						p.next()
					}
					continue
				}
				if lk == "level" {
					p.next()
					if p.inSection() {
						cfg.Global.LogLevel = strings.ToLower(unquote(p.cur.Value))
						p.next()
					}
					continue
				}
				p.next()
				break
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
			if p.inSection() {
				sk := strings.ToLower(unquote(p.cur.Value))
				p.next()
				if sk == "socket" && p.inSection() {
					cfg.Global.StatsSocket = unquote(p.cur.Value)
					p.next()
				} else if sk == "timeout" {
					if d, ok := p.parseDurationSection(); ok {
						cfg.Global.StatsTimeout = d
					}
				}
			}
		case "grace":
			if p.inSection() {
				gk := strings.ToLower(unquote(p.cur.Value))
				p.next()
				if gk == "shutdown" {
					if d, ok := p.parseDurationSection(); ok {
						cfg.Global.GraceShutdown = d
					}
				}
			}
		case "chroot":
			if p.inSection() {
				cfg.Global.Chroot = unquote(p.cur.Value)
				p.next()
			}
		case "user":
			if p.inSection() {
				cfg.Global.User = unquote(p.cur.Value)
				p.next()
			}
		case "group":
			if p.inSection() {
				cfg.Global.Group = unquote(p.cur.Value)
				p.next()
			}
		case "daemon":
			cfg.Global.Daemon = true
		default:
			if p.inSection() {
				p.next()
			}
		}
	}
	return nil
}

func (p *Parser) parseDefaults(cfg *Config) error {
	for p.inSection() {
		kw := strings.ToLower(p.cur.Value)
		p.next()
		switch kw {
		case "mode":
			if p.inSection() {
				cfg.Defaults.Mode = strings.ToLower(unquote(p.cur.Value))
				p.next()
			}
		case "balance":
			if p.inSection() {
				cfg.Defaults.Balance = strings.ToLower(unquote(p.cur.Value))
				p.next()
			}
		case "timeout":
			if p.inSection() {
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
			if p.inSection() {
				okw := strings.ToLower(unquote(p.cur.Value))
				p.next()
				switch okw {
				case "tcpka", "tcpkeepalive":
					cfg.Defaults.OptionTCPKeepAlive = true
				}
			}
		case "re-use-port", "reuseport":
			cfg.Defaults.ReUsePort = true
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
			if p.inSection() {
				p.next()
			}
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
		p.next()
		switch kw {
		case "mode":
			if p.inSection() {
				fe.Mode = strings.ToLower(unquote(p.cur.Value))
				p.next()
			}
		case "bind":
			if p.inSection() {
				bs := unquote(p.cur.Value)
				p.next()
				b, err := parseBindStr(bs)
				if err != nil {
					return fe, err
				}
				fe.Bind = append(fe.Bind, b)
			}
		case "default_backend":
			if p.inSection() {
				fe.DefaultBackend = unquote(p.cur.Value)
				p.next()
			}
		case "maxconn":
			if v, ok := p.parseIntSection(); ok {
				fe.MaxConn = v
			}
		case "timeout":
			if p.inSection() {
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
			if p.inSection() {
				okw := strings.ToLower(unquote(p.cur.Value))
				p.next()
				switch okw {
				case "tcpka", "tcpkeepalive":
					fe.OptionTCPKeepAlive = true
				}
			}
		case "acl":
			if p.inSection() {
				an := unquote(p.cur.Value)
				p.next()
				if p.inSection() {
					cr := strings.ToLower(unquote(p.cur.Value))
					p.next()
					var val string
					if p.inSection() {
						val = unquote(p.cur.Value)
						p.next()
					}
					fe.ACLs = append(fe.ACLs, ACL{Name: an, Criterion: cr, Value: val})
				}
			}
		case "use_backend":
			if p.inSection() {
				bn := unquote(p.cur.Value)
				p.next()
				var cond string
				if p.inSection() {
					c1 := strings.ToLower(unquote(p.cur.Value))
					if c1 == "if" || c1 == "unless" {
						p.next()
						if p.inSection() {
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
		default:
			if p.inSection() {
				p.next()
			}
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
		p.next()
		switch kw {
		case "mode":
			if p.inSection() {
				be.Mode = strings.ToLower(unquote(p.cur.Value))
				p.next()
			}
		case "balance":
			if p.inSection() {
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
			if p.inSection() {
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
			if p.inSection() {
				okw := strings.ToLower(unquote(p.cur.Value))
				p.next()
				switch okw {
				case "tcpka", "tcpkeepalive":
					be.OptionTCPKeepAlive = true
				}
			}
		case "re-use-port", "reuseport":
			be.ReUsePortBackend = true
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
			if p.inSection() {
				p.next()
			}
		}
	}
	return be, nil
}

func (p *Parser) parseServerLineSection() (Server, error) {
	var s Server
	if p.cur.Type != TokenWord {
		return s, fmt.Errorf("server: expected name")
	}
	s.Name = unquote(p.cur.Value)
	p.next()
	if !p.inSection() {
		return s, fmt.Errorf("server %s: expected addr", s.Name)
	}
	s.Addr = unquote(p.cur.Value)
	p.next()
	for p.inSection() {
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
	if p.cur.Type != TokenWord || !p.cur.Indented {
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
	if p.cur.Type != TokenWord || !p.cur.Indented {
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

func parseBindStr(s string) (Bind, error) {
	var b Bind
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		s = s[:i]
	}
	if s == "*" {
		s = "0.0.0.0:0"
	}
	if strings.HasPrefix(s, "*:") {
		s = "0.0.0.0:" + s[2:]
	}
	if strings.HasPrefix(s, "::") && !strings.HasPrefix(s, "[::]") {
		s = "[::]:" + strings.TrimPrefix(s, "::")
	}
	b.Addr = s
	if strings.Contains(s, ":") {
		if strings.Contains(s, "[") {
			rb := strings.Index(s, "]")
			if rb >= 0 && len(s) > rb+1 && s[rb+1] == ':' {
				if pv, err := strconv.Atoi(s[rb+2:]); err == nil {
					b.Port = pv
				} else {
					return b, fmt.Errorf("invalid port in bind: %q", s)
				}
			} else {
				return b, fmt.Errorf("invalid IPv6 bind format: %q", s)
			}
			host := ""
			if rb >= 1 {
				host = s[1:rb]
			}
			if ip := net.ParseIP(host); ip == nil {
				return b, fmt.Errorf("invalid IPv6 address in bind: %q", s)
			}
		} else {
			parts := strings.Split(s, ":")
			if len(parts) == 2 {
				if pv, err := strconv.Atoi(parts[1]); err == nil {
					b.Port = pv
				} else {
					return b, fmt.Errorf("invalid port in bind: %q", s)
				}
				if ip := net.ParseIP(parts[0]); ip == nil {
					return b, fmt.Errorf("invalid IPv4 address in bind: %q", s)
				}
			} else {
				return b, fmt.Errorf("invalid bind address format: %q", s)
			}
		}
	} else {
		return b, fmt.Errorf("bind address missing port: %q", s)
	}
	return b, nil
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