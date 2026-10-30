package balancer

import "sync/atomic"

// roundRobin rotates over all servers of the backend and skips the ones the
// health checker marked down. Backup servers are only used when every active
// server is down.
type roundRobin struct {
	srvs []Server
	idx  atomic.Uint64
}

func NewRoundRobin(srvs []Server) *roundRobin {
	return &roundRobin{srvs: srvs}
}

func (r *roundRobin) Next(_ string) (Server, error) {
	n := uint64(len(r.srvs))
	if n == 0 {
		return nil, ErrNoHealthy
	}
	start := r.idx.Add(1) - 1
	for k := uint64(0); k < n; k++ {
		s := r.srvs[(start+k)%n]
		if s.Up() && !s.Backup() {
			return s, nil
		}
	}
	for k := uint64(0); k < n; k++ {
		s := r.srvs[(start+k)%n]
		if s.Up() {
			return s, nil
		}
	}
	return nil, ErrNoHealthy
}

func (r *roundRobin) Reset() {
	r.idx.Store(0)
}
