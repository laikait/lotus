package server

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/laikait/lotus/internal/cache"
	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/metrics"
	"github.com/laikait/lotus/internal/vhost"
)

// Response cache (docs/architecture/cache.md).
//
// A route with `cache "zone"` keeps the responses of its proxy or PHP in the
// zone's memory and answers repeated GET requests from there. The zones belong
// to the server, not to a configuration generation: a reload keeps what is
// stored (and changes the limits of a zone that was resized), and only a zone
// that is no longer in the configuration is dropped.

// cacheCall is what a request needs to use the cache of its route.
type cacheCall struct {
	store *cache.Store
	opts  cache.Options
	key   cache.Key
}

// serve runs the cache for the request: fetch is called when the response is
// not there.
func (cc *cacheCall) serve(w http.ResponseWriter, r *http.Request, fetch func(http.ResponseWriter)) {
	cc.store.Serve(w, r, cc.key, cc.opts, fetch)
}

func cacheOptions(rc *model.RouteCache) cache.Options {
	o := cache.Options{
		TTL:          time.Duration(rc.TTL),
		NegativeTTL:  time.Duration(rc.NegativeTTL),
		StaleIfError: time.Duration(rc.StaleIfError),
		LockTimeout:  time.Duration(rc.LockTimeout),
	}
	switch rc.Cookies {
	case model.CookiesBypassNone:
		o.Cookies = cache.CookiesIgnore
	case model.CookiesBypassNamed:
		o.Cookies, o.CookieNames = cache.CookiesNamed, rc.BypassCookies
	}
	return o
}

// cacheFor returns the cache a request uses, or nil when its route has none.
// orig is the request as the client sent it: the key comes from it, not from
// the rewritten request the backend gets, so that the same URL always finds the
// same entry.
func (rt *siteRuntime) cacheFor(route *model.Route, orig *http.Request, scheme string) *cacheCall {
	if route == nil || route.Cache == nil {
		return nil
	}
	store := rt.caches[route.Cache.Zone]
	if store == nil {
		return nil
	}
	host, ok := vhost.NormalizeHost(orig.Host)
	if !ok {
		return nil
	}
	return &cacheCall{
		store: store,
		opts:  cacheOptions(route.Cache),
		key:   cache.Key{Host: host, Path: orig.URL.EscapedPath(), Query: orig.URL.RawQuery, Scheme: scheme},
	}
}

// cacheStore returns the store of a zone, creating it the first time the zone
// is seen. Limits of an existing store are applied by syncCaches, once the
// configuration they belong to is published.
func (s *Server) cacheStore(z model.CacheZone) *cache.Store {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if st, ok := s.caches[z.Name]; ok {
		return st
	}
	if s.caches == nil {
		s.caches = map[string]*cache.Store{}
	}
	st := cache.New(z.MaxSize, z.MaxEntrySize)
	s.caches[z.Name] = st
	return st
}

// syncCaches makes the stores match a configuration that has just been
// published: new limits are applied and zones that are gone are dropped.
func (s *Server) syncCaches(cfg *model.Config) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	want := map[string]bool{}
	for _, z := range cfg.Caches {
		want[z.Name] = true
		if st, ok := s.caches[z.Name]; ok {
			st.Configure(z.MaxSize, z.MaxEntrySize)
		}
	}
	for name := range s.caches {
		if !want[name] {
			delete(s.caches, name)
		}
	}
}

// CacheInfo describes a cache zone for `lotus cache status` and the metrics.
type CacheInfo struct {
	Zone         string
	Entries      int
	Bytes        int64
	MaxBytes     int64
	MaxEntrySize int64
	Hits         uint64
	Misses       uint64
	Stale        uint64
	Bypass       uint64
	Stored       uint64
	Evicted      uint64
	Purged       uint64
}

// ErrNoCacheZone is returned for a zone name that doesn't exist.
var ErrNoCacheZone = errors.New("no such cache zone")

// ErrInvalidPurge is returned for a purge that doesn't say clearly what to remove.
var ErrInvalidPurge = errors.New("invalid purge")

// Caches describes the cache zones of the current configuration, or only zone
// when it is not empty.
func (s *Server) Caches(zone string) ([]CacheInfo, error) {
	s.cacheMu.Lock()
	names := make([]string, 0, len(s.caches))
	for name := range s.caches {
		if zone == "" || name == zone {
			names = append(names, name)
		}
	}
	stores := make(map[string]*cache.Store, len(names))
	for _, n := range names {
		stores[n] = s.caches[n]
	}
	s.cacheMu.Unlock()
	if zone != "" && len(names) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrNoCacheZone, zone)
	}
	sort.Strings(names)
	out := make([]CacheInfo, 0, len(names))
	for _, n := range names {
		st := stores[n].Stats()
		out = append(out, CacheInfo{
			Zone: n, Entries: st.Entries, Bytes: st.Bytes, MaxBytes: st.Max, MaxEntrySize: st.MaxEntry,
			Hits: st.Hits, Misses: st.Misses, Stale: st.Stale, Bypass: st.Bypass,
			Stored: st.Stored, Evicted: st.Evicted, Purged: st.Purged,
		})
	}
	return out, nil
}

// PurgeCache removes entries from a zone and says how many it removed. An empty
// zone name means the only zone, when there is just one.
func (s *Server) PurgeCache(zone string, m cache.Match) (int, error) {
	s.cacheMu.Lock()
	if zone == "" && len(s.caches) == 1 {
		for name := range s.caches {
			zone = name
		}
	}
	st := s.caches[zone]
	known := make([]string, 0, len(s.caches))
	for name := range s.caches {
		known = append(known, name)
	}
	s.cacheMu.Unlock()
	sort.Strings(known)
	if zone == "" {
		return 0, fmt.Errorf("%w: name the zone (zones: %s)", ErrInvalidPurge, strings.Join(known, ", "))
	}
	if st == nil {
		return 0, fmt.Errorf("%w: %q", ErrNoCacheZone, zone)
	}
	if !m.All && m.Path == "" && m.Prefix == "" {
		return 0, fmt.Errorf("%w: say what to purge: a path, a prefix, or all", ErrInvalidPurge)
	}
	if m.All && (m.Path != "" || m.Prefix != "" || m.Host != "") {
		return 0, fmt.Errorf("%w: all can't be combined with a host, a path or a prefix", ErrInvalidPurge)
	}
	if m.Path != "" && m.Prefix != "" {
		return 0, fmt.Errorf("%w: give a path or a prefix, not both", ErrInvalidPurge)
	}
	for _, p := range []string{m.Path, m.Prefix} {
		if p != "" && !strings.HasPrefix(p, "/") {
			return 0, fmt.Errorf("%w: %q is not a URL path: it must start with /", ErrInvalidPurge, p)
		}
	}
	return st.Purge(m), nil
}

// writeCacheMetrics reports the cache zones. The labels are zone names from the
// configuration, never anything a client sent.
func (s *Server) writeCacheMetrics(w *metrics.Writer) {
	infos, _ := s.Caches("")
	if len(infos) == 0 {
		return
	}
	family := func(name, kind, help string, v func(CacheInfo) float64) {
		w.Family(name, kind, help)
		for _, i := range infos {
			w.Sample(v(i), metrics.L("zone", i.Zone))
		}
	}
	w.Family("lotus_cache_requests_total", "counter", "Requests of cached routes, by zone and result: hit (answered from the cache), miss (went to the backend), stale (a backend error was covered by an old entry), bypass (a request with credentials or cookies).")
	for _, i := range infos {
		for _, r := range []struct {
			result string
			n      uint64
		}{{"hit", i.Hits}, {"miss", i.Misses}, {"stale", i.Stale}, {"bypass", i.Bypass}} {
			w.Sample(float64(r.n), metrics.L("zone", i.Zone), metrics.L("result", r.result))
		}
	}
	family("lotus_cache_entries", "gauge", "Responses stored in the zone.", func(i CacheInfo) float64 { return float64(i.Entries) })
	family("lotus_cache_size_bytes", "gauge", "Memory used by the stored responses.", func(i CacheInfo) float64 { return float64(i.Bytes) })
	family("lotus_cache_max_size_bytes", "gauge", "The zone's max_size.", func(i CacheInfo) float64 { return float64(i.MaxBytes) })
	family("lotus_cache_stored_total", "counter", "Responses added to the zone.", func(i CacheInfo) float64 { return float64(i.Stored) })
	family("lotus_cache_evictions_total", "counter", "Responses dropped to make room. A steady rate means the zone is too small.", func(i CacheInfo) float64 { return float64(i.Evicted) })
	family("lotus_cache_purged_total", "counter", "Responses removed by a purge or by a write to their URL.", func(i CacheInfo) float64 { return float64(i.Purged) })
}
