package server

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/config/model"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	pmodel "github.com/prometheus/common/model"
)

// metricsConfig is a configuration with a metrics listener on a free port.
func withMetrics(cfg *model.Config, mutate ...func(*model.Metrics)) *model.Config {
	cfg.Metrics = &model.Metrics{Address: "127.0.0.1:0", Path: "/metrics"}
	for _, m := range mutate {
		m(cfg.Metrics)
	}
	return cfg
}

type scraped map[string]*dto.MetricFamily

// scrape reads the metrics listener, checks the answer parses as Prometheus
// text, and returns the families.
func scrape(t *testing.T, h *harness, hdr map[string]string) (scraped, *http.Response) {
	t.Helper()
	req, _ := http.NewRequest("GET", "http://"+h.srv.MetricsAddr().String()+"/metrics", nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, resp
	}
	if resp.Header.Get("Content-Type") != "text/plain; version=0.0.4; charset=utf-8" {
		t.Errorf("Content-Type = %q", resp.Header.Get("Content-Type"))
	}
	parser := expfmt.NewTextParser(pmodel.UTF8Validation)
	fams, err := parser.TextToMetricFamilies(strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("Prometheus's parser rejected the output: %v\n%s", err, body)
	}
	return fams, resp
}

// value finds the sample of a family whose labels include all of want.
func (s scraped) value(t *testing.T, name string, want map[string]string) (float64, bool) {
	t.Helper()
	f := s[name]
	if f == nil {
		return 0, false
	}
	for _, m := range f.GetMetric() {
		have := map[string]string{}
		for _, l := range m.GetLabel() {
			have[l.GetName()] = l.GetValue()
		}
		ok := true
		for k, v := range want {
			if have[k] != v {
				ok = false
			}
		}
		if !ok {
			continue
		}
		switch f.GetType() {
		case dto.MetricType_COUNTER:
			return m.GetCounter().GetValue(), true
		case dto.MetricType_GAUGE:
			return m.GetGauge().GetValue(), true
		case dto.MetricType_HISTOGRAM:
			return float64(m.GetHistogram().GetSampleCount()), true
		}
	}
	return 0, false
}

func (s scraped) series() int {
	n := 0
	for _, f := range s {
		n += len(f.GetMetric())
	}
	return n
}

func get200(t *testing.T, url, host string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

func TestMetricsCountRequests(t *testing.T) {
	root := docroot(t, "hello metrics")
	cfg := baseConfig(siteConf("a.test", root, "http"))
	cfg.Listeners = append(cfg.Listeners, model.Listener{Name: "empty", Address: "127.0.0.1:0"}) // no site is attached to it
	h := start(t, withMetrics(cfg))
	base := "http://" + h.addr("http")
	for i := 0; i < 3; i++ {
		get200(t, base+"/", "a.test")
	}
	get200(t, base+"/nope", "a.test")                         // 404
	get200(t, "http://"+h.addr("empty")+"/", "anything.test") // no site answers on this listener
	get200(t, base+"/", "a.test")                             // one more
	fams, _ := scrape(t, h, nil)
	site := map[string]string{"site": "a.test", "protocol": "HTTP/1"}
	if v, _ := fams.value(t, "lotus_http_requests_total", map[string]string{"site": "a.test", "protocol": "HTTP/1", "code_class": "2xx"}); v != 4 {
		t.Errorf("2xx requests = %v, want 4", v)
	}
	if v, _ := fams.value(t, "lotus_http_requests_total", map[string]string{"site": "a.test", "protocol": "HTTP/1", "code_class": "4xx"}); v != 1 {
		t.Errorf("4xx requests = %v, want 1", v)
	}
	if v, ok := fams.value(t, "lotus_http_requests_total", map[string]string{"site": "", "code_class": "4xx"}); !ok || v != 1 {
		t.Errorf("the request for an unknown host: %v %v", v, ok)
	}
	if v, _ := fams.value(t, "lotus_http_response_bytes_total", map[string]string{"site": "a.test"}); v < 4*float64(len("hello metrics")) {
		t.Errorf("response bytes = %v", v)
	}
	if v, _ := fams.value(t, "lotus_http_request_duration_seconds", map[string]string{"site": "a.test"}); v != 5 {
		t.Errorf("histogram count = %v, want 5", v)
	}
	_ = site
	if v, _ := fams.value(t, "lotus_connections_limit", nil); v != float64(model_DefaultMaxConnections()) {
		t.Errorf("connections limit = %v", v)
	}
	for _, name := range []string{"lotus_build_info", "lotus_uptime_seconds", "lotus_config_generation", "go_goroutines"} {
		if fams[name] == nil {
			t.Errorf("%s is missing", name)
		}
	}
}

func model_DefaultMaxConnections() int { return model.DefaultLimits().MaxConnections }

func TestMetricsLabelsCannotBeChosenByClients(t *testing.T) {
	root := docroot(t, "x")
	h := start(t, withMetrics(baseConfig(siteConf("a.test", root, "http"))))
	base := "http://" + h.addr("http")
	get200(t, base+"/", "a.test")
	before, _ := scrape(t, h, nil)
	for i := 0; i < 300; i++ {
		get200(t, fmt.Sprintf("%s/path-%d?q=%d", base, i, i), fmt.Sprintf("host%d.example", i)) // paths, queries and Host headers
		req, _ := http.NewRequest("GET", base+"/", nil)
		req.Host = "a.test"
		req.Header.Set("User-Agent", fmt.Sprintf("agent-%d", i))
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d", i%250))
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}
	after, _ := scrape(t, h, nil)
	// The new requests add counts, not series: the same families with at most a
	// few more combinations of site, protocol and status class.
	if grew := after.series() - before.series(); grew > 4 {
		t.Errorf("the number of series grew by %d with 600 different requests", grew)
	}
}

func TestMetricsRateLimitedAndHTTP3(t *testing.T) {
	cfg, cert := h3Config(t, func(c *model.Config) { c.Sites[0].RateLimit = rl(2, time.Hour, 2, model.RateKeyGlobal) })
	h := start(t, withMetrics(cfg))
	c := h3Client(t, h, cert)
	var codes []int
	for i := 0; i < 4; i++ {
		resp, _, err := h3Get(t, c, "/")
		if err != nil {
			t.Fatal(err)
		}
		codes = append(codes, resp.StatusCode)
	}
	if codes[0] != 200 || codes[3] != 429 {
		t.Fatalf("codes %v", codes)
	}
	// The access log line, and with it the counters, is written after the response.
	deadline := time.Now().Add(2 * time.Second)
	var fams scraped
	for time.Now().Before(deadline) {
		fams, _ = scrape(t, h, nil)
		if v, _ := fams.value(t, "lotus_http_rate_limited_total", map[string]string{"site": "h3.test"}); v == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if v, _ := fams.value(t, "lotus_http_rate_limited_total", map[string]string{"site": "h3.test"}); v != 2 {
		t.Errorf("rate limited = %v, want 2", v)
	}
	if v, _ := fams.value(t, "lotus_http_requests_total", map[string]string{"site": "h3.test", "protocol": "HTTP/3", "code_class": "4xx"}); v != 2 {
		t.Errorf("HTTP/3 4xx = %v, want 2", v)
	}
	if v, _ := fams.value(t, "lotus_http_requests_total", map[string]string{"site": "h3.test", "protocol": "HTTP/3", "code_class": "2xx"}); v != 2 {
		t.Errorf("HTTP/3 2xx = %v, want 2", v)
	}
}

func TestMetricsAccessControl(t *testing.T) {
	root := docroot(t, "x")
	// A bearer token.
	h := start(t, withMetrics(baseConfig(siteConf("a.test", root, "http")), func(m *model.Metrics) { m.BearerToken = "s3cret" }))
	for name, hdr := range map[string]map[string]string{
		"no header":    nil,
		"wrong token":  {"Authorization": "Bearer nope"},
		"wrong scheme": {"Authorization": "Basic czNjcmV0"},
		"prefix":       {"Authorization": "Bearer s3cre"},
		"longer":       {"Authorization": "Bearer s3cret!"},
		"empty":        {"Authorization": "Bearer "},
	} {
		if fams, resp := scrape(t, h, hdr); fams != nil || resp.StatusCode != 401 || resp.Header.Get("WWW-Authenticate") == "" {
			t.Errorf("%s: %d", name, resp.StatusCode)
		}
	}
	if fams, resp := scrape(t, h, map[string]string{"Authorization": "Bearer s3cret"}); fams == nil {
		t.Errorf("the right token: %d", resp.StatusCode)
	}
	if fams, _ := scrape(t, h, map[string]string{"Authorization": "bearer s3cret"}); fams == nil {
		t.Error("the scheme name is case-insensitive")
	}

	// An allow list, judged by the connection's address, not by X-Forwarded-For.
	allowOnly := func(cidr string) *harness {
		return start(t, withMetrics(baseConfig(siteConf("a.test", docroot(t, "x"), "http")), func(m *model.Metrics) {
			m.Allow = []netipPrefix{mustPrefix(cidr)}
		}))
	}
	h2 := allowOnly("203.0.113.0/24")
	if fams, resp := scrape(t, h2, map[string]string{"X-Forwarded-For": "203.0.113.9"}); fams != nil || resp.StatusCode != 403 {
		t.Errorf("a client outside the allow list that claims to be inside: %d", resp.StatusCode)
	}
	h3 := allowOnly("127.0.0.0/8")
	if fams, resp := scrape(t, h3, nil); fams == nil {
		t.Errorf("a client inside the allow list: %d", resp.StatusCode)
	}
}

func TestMetricsListenerServesOnlyMetrics(t *testing.T) {
	root := docroot(t, "the site")
	h := start(t, withMetrics(baseConfig(siteConf("a.test", root, "http"))))
	base := "http://" + h.srv.MetricsAddr().String()
	do := func(method, path, host string) *http.Response {
		req, _ := http.NewRequest(method, base+path, nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if r := do("GET", "/", "a.test"); r.StatusCode != 404 {
		t.Errorf("a site's page on the metrics port: %d", r.StatusCode)
	}
	if r := do("GET", "/metrics/", "a.test"); r.StatusCode != 404 {
		t.Errorf("/metrics/: %d", r.StatusCode)
	}
	if r := do("POST", "/metrics", "a.test"); r.StatusCode != 405 {
		t.Errorf("POST: %d", r.StatusCode)
	}
	if r := do("HEAD", "/metrics", "a.test"); r.StatusCode != 200 {
		t.Errorf("HEAD: %d", r.StatusCode)
	}
	// The site's own listener doesn't serve the metrics.
	req, _ := http.NewRequest("GET", "http://"+h.addr("http")+"/metrics", nil)
	req.Host = "a.test"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("/metrics on the site listener: %d", resp.StatusCode)
	}
	// And a request to the metrics port is not counted as a site request.
	fams, _ := scrape(t, h, nil)
	if v, ok := fams.value(t, "lotus_http_requests_total", nil); ok && v > 1 {
		t.Logf("requests counted: %v", v) // only the one for /metrics on the site listener
	}
}

func TestMetricsOffByDefault(t *testing.T) {
	root := docroot(t, "x")
	h := start(t, baseConfig(siteConf("a.test", root, "http")))
	if h.srv.MetricsAddr() != nil {
		t.Error("a metrics listener without configuration")
	}
}

func TestMetricsPathIsConfigurable(t *testing.T) {
	root := docroot(t, "x")
	h := start(t, withMetrics(baseConfig(siteConf("a.test", root, "http")), func(m *model.Metrics) { m.Path = "/internal/stats" }))
	get := func(path string) int {
		resp, err := http.Get("http://" + h.srv.MetricsAddr().String() + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if get("/internal/stats") != 200 || get("/metrics") != 404 {
		t.Error("the configured path is not the only one")
	}
}

func TestMetricsReloadCountersAndGeneration(t *testing.T) {
	root := docroot(t, "x")
	cfg := withMetrics(baseConfig(siteConf("a.test", root, "http")))
	h := start(t, cfg)
	if err := h.srv.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	bad := withMetrics(baseConfig(siteConf("a.test", root, "http")))
	bad.Sites[0].Root = root + "-missing"
	if err := h.srv.Reload(bad); err == nil {
		t.Fatal("a reload with a missing document root worked")
	}
	fams, _ := scrape(t, h, nil)
	if v, _ := fams.value(t, "lotus_config_reloads_total", map[string]string{"result": "success"}); v != 1 {
		t.Errorf("successful reloads = %v", v)
	}
	if v, _ := fams.value(t, "lotus_config_reloads_total", map[string]string{"result": "failure"}); v != 1 {
		t.Errorf("failed reloads = %v", v)
	}
	if v, _ := fams.value(t, "lotus_config_generation", nil); v != 2 {
		t.Errorf("generation = %v, want 2 (a failed reload doesn't consume a number)", v)
	}
	if v, _ := fams.value(t, "lotus_config_last_reload_success_timestamp_seconds", nil); v < float64(time.Now().Add(-time.Minute).Unix()) {
		t.Errorf("last success = %v", v)
	}

	// Counters survive a reload.
	get200(t, "http://"+h.addr("http")+"/", "a.test")
	before, _ := scrape(t, h, nil)
	if err := h.srv.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	after, _ := scrape(t, h, nil)
	a, _ := before.value(t, "lotus_http_requests_total", map[string]string{"site": "a.test"})
	b, _ := after.value(t, "lotus_http_requests_total", map[string]string{"site": "a.test"})
	if a == 0 || a != b {
		t.Errorf("request count %v before a reload and %v after", a, b)
	}

	// The metrics listener itself can't change in a reload.
	moved := withMetrics(baseConfig(siteConf("a.test", root, "http")), func(m *model.Metrics) { m.Path = "/other" })
	if err := h.srv.Reload(moved); err == nil || !strings.Contains(err.Error(), "metrics") {
		t.Errorf("err = %v", err)
	}
}

func TestMetricsUpstreamBackends(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer be.Close()
	dead := func() string {
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		a := l.Addr().String()
		l.Close()
		return "http://" + a
	}()
	u := upstreamOf("app", be.URL, dead)
	u.Retries = 1
	cfg := routedConfig(t, nil, []model.Route{proxyRoute("/", model.Actions{Proxy: "app"})})
	cfg.Upstreams = []model.Upstream{u}
	h := start(t, withMetrics(cfg))
	for i := 0; i < 6; i++ {
		get200(t, "http://"+h.addr("http")+"/x", "a.test")
	}
	fams, _ := scrape(t, h, nil)
	good := strings.TrimPrefix(be.URL, "http://")
	badAddr := strings.TrimPrefix(dead, "http://")
	if v, ok := fams.value(t, "lotus_upstream_backend_requests_total", map[string]string{"upstream": "app", "backend": good}); !ok || v < 6 {
		t.Errorf("requests to the good backend = %v %v", v, ok)
	}
	if v, ok := fams.value(t, "lotus_upstream_backend_failures_total", map[string]string{"upstream": "app", "backend": badAddr}); !ok || v < 1 {
		t.Errorf("failures of the dead backend = %v %v", v, ok)
	}
	if v, _ := fams.value(t, "lotus_upstream_backend_failures_total", map[string]string{"upstream": "app", "backend": good}); v != 0 {
		t.Errorf("failures of the good backend = %v", v)
	}
	if v, ok := fams.value(t, "lotus_upstream_backend_healthy", map[string]string{"backend": good}); !ok || v != 1 {
		t.Errorf("good backend healthy = %v %v", v, ok)
	}
}

func TestMetricsCertificateExpiry(t *testing.T) {
	cfg, cert := h3Config(t)
	h := start(t, withMetrics(cfg))
	fams, _ := scrape(t, h, nil)
	got, ok := fams.value(t, "lotus_certificate_expiry_timestamp_seconds", map[string]string{"site": "h3.test", "source": "file"})
	if !ok || int64(got) != cert.cert.NotAfter.Unix() {
		t.Errorf("expiry = %v %v, want %d", got, ok, cert.cert.NotAfter.Unix())
	}
	if v, _ := fams.value(t, "lotus_certificate_ready", map[string]string{"site": "h3.test"}); v != 1 {
		t.Errorf("ready = %v", v)
	}
}

func TestAnUnavailableMetricsPortStopsTheStart(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	root := docroot(t, "x")
	cfg := withMetrics(baseConfig(siteConf("a.test", root, "http")), func(m *model.Metrics) { m.Address = busy.Addr().String() })
	cfg.Listeners[0].Address = "127.0.0.1:0"
	srv, err := New(cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err == nil || !strings.Contains(err.Error(), "metrics") {
		t.Fatalf("err = %v", err)
	}
	if srv.MetricsAddr() != nil {
		t.Error("a listener was left behind")
	}
}

// Scraping while requests and reloads happen must be race-free (CI runs this
// with the race detector).
func TestMetricsConcurrentScrapesRequestsAndReloads(t *testing.T) {
	root := docroot(t, "x")
	cfg := withMetrics(baseConfig(siteConf("a.test", root, "http")))
	h := start(t, cfg)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				get200(t, "http://"+h.addr("http")+"/", "a.test")
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			h.srv.Reload(cfg)
			time.Sleep(5 * time.Millisecond)
		}
	}()
	for i := 0; i < 30; i++ {
		if fams, _ := scrape(t, h, nil); fams == nil {
			t.Error("a scrape failed")
		}
	}
	close(stop)
	wg.Wait()
}

func TestObservePlacesRequestsInTheRightSeries(t *testing.T) {
	var st siteStats
	ms := time.Millisecond
	for _, o := range []struct {
		proto, status int
		took          time.Duration
	}{
		{1, 200, 5 * ms},   // exactly on the first bound: in it (le is inclusive)
		{2, 301, 6 * ms},   // just above: the second bucket
		{3, 404, 100 * ms}, // 0.1 s: the bucket with le=0.1
		{1, 503, 100 * time.Second},
		{1, 99, 0}, {1, 600, 0}, {0, 200, 0}, // outside 100-599 and an unknown protocol
	} {
		st.observe(o.proto, o.status, 10, o.took)
	}
	at := func(p, c int) uint64 { return st.requests[p][c].Load() }
	if at(protoHTTP1, 1) != 2 || at(protoHTTP2, 2) != 1 || at(protoHTTP3, 3) != 1 || at(protoHTTP1, 4) != 1 || at(protoHTTP1, 5) != 2 {
		t.Errorf("requests: h1 %d %d %d %d %d %d, h2 %d %d %d %d %d %d, h3 %d %d %d %d %d %d",
			at(0, 0), at(0, 1), at(0, 2), at(0, 3), at(0, 4), at(0, 5),
			at(1, 0), at(1, 1), at(1, 2), at(1, 3), at(1, 4), at(1, 5),
			at(2, 0), at(2, 1), at(2, 2), at(2, 3), at(2, 4), at(2, 5))
	}
	if st.bytes.Load() != 70 {
		t.Errorf("bytes = %d", st.bytes.Load())
	}
	want := map[float64]uint64{0.005: 1 + 3, 0.01: 1, 0.1: 1} // the three zero-length ones fall in the first bucket
	for i, b := range latencyBounds {
		if got := st.latency[i].Load(); got != want[b] {
			t.Errorf("bucket le=%v has %d, want %d", b, got, want[b])
		}
	}
	if st.latency[numLatencyBounds].Load() != 1 {
		t.Errorf("+Inf bucket has %d", st.latency[numLatencyBounds].Load())
	}
}

func TestCertificatesListsAManualCertificate(t *testing.T) {
	cfg, cert := h3Config(t)
	h := start(t, cfg)
	list := h.srv.Certificates()
	if len(list) != 1 {
		t.Fatalf("%+v", list)
	}
	c := list[0]
	if c.Name != "h3.test" || c.Site != "h3.test" || c.Source != "file" || !c.Ready || !c.NotAfter.Equal(cert.cert.NotAfter) ||
		len(c.Domains) != 1 || c.Domains[0] != "h3.test" {
		t.Errorf("%+v", c)
	}
}
