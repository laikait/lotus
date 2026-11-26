package server

import (
	"sort"
	"time"
)

// SiteTraffic is the running totals of one site since the server started (a reload does not reset them). A caller
// that wants a rate keeps two snapshots and divides the difference by the time between them.
type SiteTraffic struct {
	Name        string // empty: requests that matched no site
	Requests    uint64
	Class       [numClasses]uint64 // 1xx 2xx 3xx 4xx 5xx other (see classNames)
	Bytes       uint64
	RateLimited uint64
	LatencySum  time.Duration // total time spent answering; divide by Requests for the average
}

// TrafficSnapshot is what the live traffic page shows: the server-wide figures and each site's totals.
type TrafficSnapshot struct {
	At              time.Time
	Uptime          time.Duration
	Connections     int // open now, counted against the limit
	ConnectionLimit int
	Rejected        uint64 // connections closed at once for a limit
	Dropped         uint64 // requests dropped before routing
	Sites           []SiteTraffic
	Unrouted        SiteTraffic
}

// ClassNames are the labels of SiteTraffic.Class, in order.
func ClassNames() []string { return classNames[:] }

// Traffic reads the counters the metrics already keep; it adds nothing to the request path.
func (s *Server) Traffic() TrafficSnapshot {
	g := s.gen.Load()
	now := time.Now()
	snap := TrafficSnapshot{
		At:              now,
		Uptime:          now.Sub(s.stats.started),
		Connections:     len(s.limiter.slots),
		ConnectionLimit: cap(s.limiter.slots),
		Rejected:        s.limiter.rejected.Load(),
		Dropped:         s.tracker.dropped.Load(),
		Unrouted:        readSiteStats("", &s.stats.unrouted),
	}
	names := make([]string, 0, len(g.cfg.Sites))
	for i := range g.cfg.Sites {
		names = append(names, g.cfg.Sites[i].Name)
	}
	sort.Strings(names)
	for _, name := range names {
		s.stats.mu.RLock()
		st := s.stats.sites[name]
		s.stats.mu.RUnlock()
		if st == nil {
			snap.Sites = append(snap.Sites, SiteTraffic{Name: name})
			continue
		}
		snap.Sites = append(snap.Sites, readSiteStats(name, st))
	}
	return snap
}

func readSiteStats(name string, st *siteStats) SiteTraffic {
	out := SiteTraffic{Name: name, Bytes: st.bytes.Load(), RateLimited: st.rateLimited.Load(), LatencySum: time.Duration(st.latencySum.Load())}
	for p := 0; p < numProtos; p++ {
		for c := 0; c < numClasses; c++ {
			v := st.requests[p][c].Load()
			out.Class[c] += v
			out.Requests += v
		}
	}
	return out
}
