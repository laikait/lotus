package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/metrics"
	"github.com/laikait/lotus/pkg/version"
)

// Metrics (docs/architecture/metrics.md).
//
// The counters live next to the code they count, as atomic integers, so
// counting a request costs a few atomic additions and no lock. On a scrape the
// collector describes them in the Prometheus text format. Series are
// per-site, per-upstream-backend and so on: names that the administrator wrote
// in the configuration. Nothing a client sends (a path, a Host header, a
// query) becomes a label, so a client can't make the series count grow.

// latencyBounds are the upper bounds, in seconds, of the request duration
// histogram. numLatencyBounds must be its length.
var latencyBounds = [numLatencyBounds]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}

const numLatencyBounds = 13

// Protocols and status classes, as indexes into siteStats.requests.
const (
	protoHTTP1 = iota
	protoHTTP2
	protoHTTP3
	numProtos
)

const numClasses = 6 // 1xx 2xx 3xx 4xx 5xx, and anything else

var (
	protoNames = [numProtos]string{"HTTP/1", "HTTP/2", "HTTP/3"}
	classNames = [numClasses]string{"1xx", "2xx", "3xx", "4xx", "5xx", "other"}
)

// siteStats counts what happens to the requests of one site. They are kept by
// the Server, not by a configuration generation, so a reload doesn't reset them.
type siteStats struct {
	requests    [numProtos][numClasses]atomic.Uint64
	bytes       atomic.Uint64
	rateLimited atomic.Uint64
	latency     [numLatencyBounds + 1]atomic.Uint64 // per bucket, not cumulative
	latencySum  atomic.Int64                        // nanoseconds
}

// observe records one finished request.
func (st *siteStats) observe(protoMajor, status int, bytes int64, took time.Duration) {
	p := protoHTTP1
	switch protoMajor {
	case 2:
		p = protoHTTP2
	case 3:
		p = protoHTTP3
	}
	c := status/100 - 1
	if c < 0 || c >= numClasses-1 {
		c = numClasses - 1
	}
	st.requests[p][c].Add(1)
	if bytes > 0 {
		st.bytes.Add(uint64(bytes))
	}
	secs := took.Seconds()
	i := sort.SearchFloat64s(latencyBounds[:], secs) // first bound >= secs
	st.latency[i].Add(1)
	st.latencySum.Add(int64(took))
}

// serverStats is the Server's own counters.
type serverStats struct {
	started     time.Time
	mu          sync.RWMutex          // guards sites
	sites       map[string]*siteStats // by site name; filled by siteStatsFor
	unrouted    siteStats             // requests that matched no site
	reloadsOK   atomic.Uint64
	reloadsFail atomic.Uint64
	lastReload  atomic.Int64 // unix seconds of the last successful reload or start
}

// siteStatsFor returns the counters of a site, creating them on first use. It
// is called while a generation is built, and a scrape may read at the same time.
func (s *Server) siteStatsFor(name string) *siteStats {
	s.stats.mu.Lock()
	defer s.stats.mu.Unlock()
	if st, ok := s.stats.sites[name]; ok {
		return st
	}
	st := &siteStats{}
	s.stats.sites[name] = st
	return st
}

// countReload records the outcome of a reload.
func (s *Server) countReload(err error) {
	if err != nil {
		s.stats.reloadsFail.Add(1)
		return
	}
	s.stats.reloadsOK.Add(1)
	s.stats.lastReload.Store(time.Now().Unix())
}

// ---- collecting ----

// WriteMetrics describes the server's state and counters to w.
func (s *Server) WriteMetrics(w *metrics.Writer) {
	g := s.gen.Load()
	now := time.Now()

	w.Gauge("lotus_build_info", "The LOTUS version, as a label; the value is always 1.", 1,
		metrics.L("version", version.Version), metrics.L("go_version", runtime.Version()))
	w.Gauge("lotus_start_time_seconds", "When the server started, in seconds since the epoch.", float64(s.stats.started.Unix()))
	w.Gauge("lotus_uptime_seconds", "Seconds since the server started.", now.Sub(s.stats.started).Seconds())

	w.Gauge("lotus_config_generation", "Number of the configuration in use: 1 at start, one more after each reload that changed it.", float64(g.number))
	w.Family("lotus_config_reloads_total", "counter", "Reloads of the configuration, by result.")
	w.Sample(float64(s.stats.reloadsOK.Load()), metrics.L("result", "success"))
	w.Sample(float64(s.stats.reloadsFail.Load()), metrics.L("result", "failure"))
	w.Gauge("lotus_config_last_reload_success_timestamp_seconds", "When the configuration was last loaded successfully (start or reload), in seconds since the epoch.", float64(s.stats.lastReload.Load()))

	w.Gauge("lotus_connections_active", "Open client connections (TCP and QUIC) counted against max_connections.", float64(len(s.limiter.slots)))
	w.Gauge("lotus_connections_limit", "The max_connections limit.", float64(cap(s.limiter.slots)))
	w.Family("lotus_connections_rejected_total", "counter", "Connections closed at once because a limit was reached, by limit.")
	w.Sample(float64(s.limiter.rejected.Load()), metrics.L("limit", "max_connections"))
	w.Sample(float64(s.limiter.rejectedIP.Load()), metrics.L("limit", "max_connections_per_ip"))
	w.Counter("lotus_requests_dropped_total", "Requests that the HTTP server rejected or saw abandoned before they were routed (malformed, too large, too slow).", float64(s.tracker.dropped.Load()))

	// Per-site request counters. Only the current configuration's sites, and only
	// series that have a value, so the output stays small.
	sites := make([]string, 0, len(g.cfg.Sites))
	for i := range g.cfg.Sites {
		sites = append(sites, g.cfg.Sites[i].Name)
	}
	sort.Strings(sites)
	type named struct {
		name string
		st   *siteStats
	}
	var all []named
	for _, name := range sites {
		s.stats.mu.RLock()
		st := s.stats.sites[name]
		s.stats.mu.RUnlock()
		if st != nil {
			all = append(all, named{name, st})
		}
	}
	all = append(all, named{"", &s.stats.unrouted}) // site="" is requests no site answered

	w.Family("lotus_http_requests_total", "counter", "Requests answered, by site, protocol and status class. site is empty for requests that matched no site.")
	for _, n := range all {
		for p := 0; p < numProtos; p++ {
			for c := 0; c < numClasses; c++ {
				if v := n.st.requests[p][c].Load(); v > 0 {
					w.Sample(float64(v), metrics.L("site", n.name), metrics.L("protocol", protoNames[p]), metrics.L("code_class", classNames[c]))
				}
			}
		}
	}
	w.Family("lotus_http_response_bytes_total", "counter", "Response body bytes sent, by site.")
	for _, n := range all {
		if v := n.st.bytes.Load(); v > 0 {
			w.Sample(float64(v), metrics.L("site", n.name))
		}
	}
	w.Family("lotus_http_rate_limited_total", "counter", "Requests answered 429 by a rate limit or a password lock-out, by site.")
	for _, n := range all {
		if v := n.st.rateLimited.Load(); v > 0 {
			w.Sample(float64(v), metrics.L("site", n.name))
		}
	}
	w.Family("lotus_http_request_duration_seconds", "histogram", "Time from the start of a request until its response was written, by site.")
	for _, n := range all {
		counts := make([]uint64, len(n.st.latency))
		var total uint64
		for i := range counts {
			counts[i] = n.st.latency[i].Load()
			total += counts[i]
		}
		if total > 0 {
			w.Histogram(latencyBounds[:], counts, time.Duration(n.st.latencySum.Load()).Seconds(), metrics.L("site", n.name))
		}
	}

	// Reverse proxy backends.
	ups := make([]string, 0, len(g.upstream))
	for name := range g.upstream {
		ups = append(ups, name)
	}
	sort.Strings(ups)
	type backendRow struct {
		upstream string
		status   string
		healthy  bool
		active   int64
		requests uint64
		failures uint64
	}
	var rows []backendRow
	for _, name := range ups {
		for _, b := range g.upstream[name].Status() {
			rows = append(rows, backendRow{name, b.Address, b.Healthy, b.Active, b.Requests, b.Failures})
		}
	}
	lab := func(r backendRow) []metrics.Label {
		return []metrics.Label{metrics.L("upstream", r.upstream), metrics.L("backend", r.status)}
	}
	w.Family("lotus_upstream_backend_healthy", "gauge", "1 if the backend takes requests, 0 if it is out of rotation (failures or a failed health check).")
	for _, r := range rows {
		v := 0.0
		if r.healthy {
			v = 1
		}
		w.Sample(v, lab(r)...)
	}
	w.Family("lotus_upstream_backend_active_requests", "gauge", "Requests in progress on the backend.")
	for _, r := range rows {
		w.Sample(float64(r.active), lab(r)...)
	}
	w.Family("lotus_upstream_backend_requests_total", "counter", "Requests sent to the backend, retries included.")
	for _, r := range rows {
		w.Sample(float64(r.requests), lab(r)...)
	}
	w.Family("lotus_upstream_backend_failures_total", "counter", "Requests to the backend that failed to complete (connection, timeout, TLS).")
	for _, r := range rows {
		w.Sample(float64(r.failures), lab(r)...)
	}

	// Managed PHP processes.
	var php []string
	for name := range g.php {
		php = append(php, name)
	}
	sort.Strings(php)
	w.Family("lotus_php_process_restarts_total", "counter", "PHP processes that LOTUS replaced after they exited or reached max_requests, by backend.")
	for _, name := range php {
		if p, ok := g.php[name].(interface{ Restarts() int64 }); ok {
			w.Sample(float64(p.Restarts()), metrics.L("backend", name))
		}
	}

	s.writeCertificateMetrics(w, g)

	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	w.Gauge("go_goroutines", "Goroutines that currently exist.", float64(runtime.NumGoroutine()))
	w.Gauge("go_memstats_alloc_bytes", "Bytes of allocated heap objects.", float64(m.Alloc))
	w.Gauge("go_memstats_heap_inuse_bytes", "Bytes in in-use heap spans.", float64(m.HeapInuse))
	w.Gauge("go_memstats_sys_bytes", "Bytes obtained from the system.", float64(m.Sys))
	w.Counter("go_gc_cycles_total", "Completed garbage collection cycles.", float64(m.NumGC))
}

// writeCertificateMetrics reports when certificates expire: the ones loaded
// from files (tls manual) and the ones obtained from a CA (tls automatic). An
// alert on the smallest expiry catches a renewal that has silently failed.
func (s *Server) writeCertificateMetrics(w *metrics.Writer, g *generation) {
	type row struct {
		name, site, source string
		notAfter           time.Time
		ready              bool
	}
	var rows []row
	for i := range g.cfg.Sites {
		site := &g.cfg.Sites[i]
		if site.TLS != model.TLSManual {
			continue
		}
		if rt := g.sites[site.Name]; rt != nil && rt.cert != nil && rt.cert.Leaf != nil {
			rows = append(rows, row{name: site.Name, site: site.Name, source: "file", notAfter: rt.cert.Leaf.NotAfter, ready: true})
		}
	}
	if m := s.acmeManager(); m != nil {
		for _, c := range m.Certificates() {
			rows = append(rows, row{name: c.Name, source: "acme", notAfter: c.NotAfter, ready: c.Ready})
		}
	}
	lab := func(r row) []metrics.Label {
		l := []metrics.Label{metrics.L("name", r.name), metrics.L("source", r.source)}
		if r.site != "" {
			l = append(l, metrics.L("site", r.site))
		}
		return l
	}
	w.Family("lotus_certificate_ready", "gauge", "1 if a certificate for the name is loaded, 0 if one is still to be obtained (tls automatic).")
	for _, r := range rows {
		v := 0.0
		if r.ready {
			v = 1
		}
		w.Sample(v, lab(r)...)
	}
	w.Family("lotus_certificate_expiry_timestamp_seconds", "gauge", "When the certificate expires, in seconds since the epoch. Alert when this is less than about 14 days away.")
	for _, r := range rows {
		if r.ready {
			w.Sample(float64(r.notAfter.Unix()), lab(r)...)
		}
	}
}

// ---- the listener ----

// metricsServer serves the metrics on their own listener.
type metricsServer struct {
	srv *http.Server
	ln  net.Listener
}

// maxScrapes is how many scrapes may run at once; further ones get 503.
const maxScrapes = 4

// startMetrics binds the metrics listener, if the configuration has one.
func (s *Server) startMetrics(cfg *model.Config) error {
	m := cfg.Metrics
	if m == nil {
		return nil
	}
	ln, err := net.Listen("tcp", m.Address)
	if err != nil {
		return fmt.Errorf("metrics listener: %w", err)
	}
	slots := make(chan struct{}, maxScrapes)
	h := http.NewServeMux()
	h.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", serverHeader)
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path != m.Path {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !metricsAllowed(m, r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if m.BearerToken != "" && !validBearer(r.Header.Get("Authorization"), string(m.BearerToken)) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="lotus metrics"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "too many scrapes at once", http.StatusServiceUnavailable)
			return
		}
		var buf strings.Builder
		mw := metrics.NewWriter(&buf)
		s.WriteMetrics(mw)
		if err := mw.Err(); err != nil {
			s.log.Error("metrics: could not render", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", metrics.ContentType)
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write([]byte(buf.String()))
	})
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
	s.metrics = &metricsServer{srv: srv, ln: ln}
	return nil
}

// metricsAllowed applies the allow list to the connection's own address. The
// forwarding headers are not consulted: a client must not be able to claim an
// address that is allowed.
func metricsAllowed(m *model.Metrics, r *http.Request) bool {
	if len(m.Allow) == 0 {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	return slices.ContainsFunc(m.Allow, func(p netip.Prefix) bool { return p.Contains(ip) })
}

// validBearer checks an Authorization header against the token, in constant
// time (the length of the token is not secret enough to matter).
func validBearer(header, token string) bool {
	const prefix = "Bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header[len(prefix):]), []byte(token)) == 1
}

func (ms *metricsServer) serve(errc chan<- error) {
	if err := ms.srv.Serve(ms.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errc <- fmt.Errorf("metrics listener: %w", err)
	}
}

func (ms *metricsServer) stop(ctx context.Context) {
	if err := ms.srv.Shutdown(ctx); err != nil {
		ms.srv.Close()
	}
}

// MetricsAddr returns where the metrics listener is bound, or nil without one.
// It is meaningful after Start.
func (s *Server) MetricsAddr() net.Addr {
	if s.metrics == nil {
		return nil
	}
	return s.metrics.ln.Addr()
}
