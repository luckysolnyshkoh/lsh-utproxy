package balancer

type leastConn struct {
	srvs []Server
}

func NewLeastConn(srvs []Server) *leastConn {
	return &leastConn{srvs: filterActive(srvs)}
}

func (l *leastConn) Next() (Server, error) {
	if len(l.srvs) == 0 {
		return nil, ErrNoHealthy
	}
	best := l.srvs[0]
	bestc := best.Active()
	for i := 1; i < len(l.srvs); i++ {
		s := l.srvs[i]
		c := s.Active()
		if c < bestc {
			bestc = c
			best = s
		}
	}
	return best, nil
}

func (l *leastConn) Reset() {}