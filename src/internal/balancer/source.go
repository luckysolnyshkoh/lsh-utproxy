package balancer

import (
	"hash/fnv"
	"net"
)

// source maps a client address to a server (sticky balancing). key is the
// client IP without the port; an empty key falls back to the first healthy
// server.
type source struct {
	srvs []Server
}

func NewSource(srvs []Server) *source {
	return &source{srvs: srvs}
}

func (s *source) Next(key string) (Server, error) {
	if best := s.pick(key, false); best != nil {
		return best, nil
	}
	if best := s.pick(key, true); best != nil {
		return best, nil
	}
	return nil, ErrNoHealthy
}

func (s *source) pick(key string, backups bool) Server {
	n := 0
	for _, srv := range s.srvs {
		if srv.Up() && srv.Backup() == backups {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	i := int(h.Sum32() % uint32(n))
	for _, srv := range s.srvs {
		if srv.Up() && srv.Backup() == backups {
			if i == 0 {
				return srv
			}
			i--
		}
	}
	return nil
}

func (s *source) Reset() {}

// HostOnly strips the port from an "host:port" or "[ipv6]:port" address so the
// balancer sticks to the source address, like HAProxy's "balance source".
func HostOnly(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}
