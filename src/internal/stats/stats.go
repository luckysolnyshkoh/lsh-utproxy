package stats

import (
	"sync/atomic"
	"time"
)

type Stats struct {
	ConnsOpen  atomic.Int64
	ConnsTotal atomic.Int64
	BytesIn    atomic.Int64
	BytesOut   atomic.Int64
	PacketsIn  atomic.Int64
	PacketsOut atomic.Int64
	Errors     atomic.Int64
	Timeouts   atomic.Int64
	Dropped    atomic.Int64
	// Blocked counts connections and datagrams refused by an allow/deny/drop
	// rule of the frontend they came in on.
	Blocked     atomic.Int64
	UDPSessions atomic.Int64
	Start       time.Time
}

func New() *Stats {
	return &Stats{Start: time.Now()}
}

func (s *Stats) UpTime() time.Duration { return time.Since(s.Start) }
