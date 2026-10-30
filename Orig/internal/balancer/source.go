package balancer

import "hash/fnv"

type source struct {
	srvs []Server
}

func NewSource(srvs []Server) *source {
	return &source{srvs: filterActive(srvs)}
}

func (s *source) Next() (Server, error) {
	if len(s.srvs) == 0 {
		return nil, ErrNoHealthy
	}
	h := fnv.New32a()
	h.Write([]byte("default"))
	i := int(h.Sum32()) % len(s.srvs)
	return s.srvs[i], nil
}

func (s *source) Reset() {}