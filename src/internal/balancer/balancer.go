package balancer

import (
	"errors"
	"lsh-utproxy/internal/config"
)

var ErrNoHealthy = errors.New("no healthy servers")

type Server interface {
	ID() string
	Addr() string
	Weight() int
	MaxConn() int
	Backup() bool
	Active() int64
	Inc() int64
	Dec() int64
	Up() bool
	SetUp(bool)
	HealthScore() int
}

type Balancer interface {
	// Next returns the next server to use. key carries the client address for
	// address-based algorithms and is ignored by the others.
	Next(key string) (Server, error)
	Reset()
}

func New(cfg *config.Backend, srvs []Server) Balancer {
	mode := cfg.Balance
	if mode == "" {
		mode = "roundrobin"
	}
	switch mode {
	case "leastconn":
		return NewLeastConn(srvs)
	case "source":
		return NewSource(srvs)
	case "roundrobin", "rr":
		fallthrough
	default:
		return NewRoundRobin(srvs)
	}
}
