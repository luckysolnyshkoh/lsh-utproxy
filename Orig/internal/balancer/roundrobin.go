package balancer

import "sync/atomic"

type roundRobin struct {
	srvs []Server
	idx  uint64
}

func NewRoundRobin(srvs []Server) *roundRobin {
	return &roundRobin{srvs: filterActive(srvs), idx: 0}
}

func filterActive(srvs []Server) []Server {
	out := make([]Server, 0, len(srvs))
	for _, s := range srvs {
		if s.Up() && !s.Backup() {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		for _, s := range srvs {
			if s.Up() {
				out = append(out, s)
			}
		}
	}
	return out
}

func (r *roundRobin) Next() (Server, error) {
	if len(r.srvs) == 0 {
		return nil, ErrNoHealthy
	}
	i := atomic.AddUint64(&r.idx, 1) - 1
	s := r.srvs[i%uint64(len(r.srvs))]
	return s, nil
}

func (r *roundRobin) Reset() {
	atomic.StoreUint64(&r.idx, 0)
}