package balancer

// leastConn picks the healthy server with the smallest number of active
// connections, scaled by weight (HAProxy: connections / weight).
type leastConn struct {
	srvs []Server
}

func NewLeastConn(srvs []Server) *leastConn {
	return &leastConn{srvs: srvs}
}

func (l *leastConn) Next(_ string) (Server, error) {
	if best := l.pick(false); best != nil {
		return best, nil
	}
	if best := l.pick(true); best != nil {
		return best, nil
	}
	return nil, ErrNoHealthy
}

// pick returns the least loaded healthy server; backups=true restricts the
// choice to backup servers, backups=false excludes them.
func (l *leastConn) pick(backups bool) Server {
	var best Server
	var bestC, bestW int64
	for _, s := range l.srvs {
		if !s.Up() || s.Backup() != backups {
			continue
		}
		w := int64(s.Weight())
		if w <= 0 {
			w = 1
		}
		c := s.Active()
		if best == nil || c*bestW < bestC*w {
			best, bestC, bestW = s, c, w
		}
	}
	return best
}

func (l *leastConn) Reset() {}
