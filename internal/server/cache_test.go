package server

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/cache"
	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/fastcgi/fcgitest"
)

// ---- helpers ----

// countedBackend is an HTTP backend that counts the requests that reach it.
type countedBackend struct {
	srv *httptest.Server
	n   atomic.Int32
}

func newCounted(t *testing.T, h http.HandlerFunc) *countedBackend {
	t.Helper()
	cb := &countedBackend{}
	cb.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cb.n.Add(1)
		h(w, r)
	}))
	t.Cleanup(cb.srv.Close)
	return cb
}

func (c *countedBackend) count() int { return int(c.n.Load()) }

func cacheRoute(rc model.RouteCache) model.Route {
	r := proxyRoute("/app/", model.Actions{Proxy: "app"})
	r.Cache = &rc
	return r
}

func defaultRouteCache() model.RouteCache {
	return model.RouteCache{Zone: "pages", TTL: model.Duration(time.Minute), LockTimeout: model.Duration(2 * time.Second), Cookies: model.CookiesBypassAny}
}

func testZone() model.CacheZone {
	return model.CacheZone{Name: "pages", MaxSize: 8 << 20, MaxEntrySize: 1 << 20}
}

// cachedConfig is a site whose /app/ route proxies to be and caches.
func cachedConfig(t *testing.T, be string, rc model.RouteCache, zone model.CacheZone) *model.Config {
	t.Helper()
	cfg := routedConfig(t, func(s *model.Site) {
		s.SecurityHeaders = []model.Header{{Name: "X-Frame-Options", Value: "DENY"}}
	}, []model.Route{cacheRoute(rc)})
	cfg.Upstreams = []model.Upstream{upstreamOf("app", be)}
	cfg.Caches = []model.CacheZone{zone}
	return cfg
}

func startCached(t *testing.T, be string, rc model.RouteCache) (*harness, string) {
	t.Helper()
	h := start(t, cachedConfig(t, be, rc, testZone()))
	return h, "http://" + h.addr("http")
}

func xc(resp *http.Response) string { return resp.Header.Get("X-Cache") }

// fakeClock is a clock tests move by hand.
type fakeClock struct{ ns atomic.Int64 }

func newClock() *fakeClock {
	c := &fakeClock{}
	c.ns.Store(time.Now().UnixNano())
	return c
}
func (c *fakeClock) now() time.Time          { return time.Unix(0, c.ns.Load()) }
func (c *fakeClock) advance(d time.Duration) { c.ns.Add(int64(d)) }

func zoneStore(h *harness, name string) *cache.Store {
	h.srv.cacheMu.Lock()
	defer h.srv.cacheMu.Unlock()
	return h.srv.caches[name]
}

func page200(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }
}

// tls0 stands for a TLS connection state in requests of tests.
var tls0 tls.ConnectionState

// ---- tests ----

func TestCacheAnswersRepeatedRequests(t *testing.T) {
	be := newCounted(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "Apache/2.4 (leaky)")
		w.Header().Set("X-Frame-Options", "ALLOWALL")
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "page %s", r.URL.RequestURI())
	})
	_, base := startCached(t, be.srv.URL, defaultRouteCache())

	first, body1 := do(t, "GET", base+"/app/x?y=1", nil, "")
	if xc(first) != "MISS" || body1 != "page /app/x?y=1" {
		t.Fatalf("first: %s %q", xc(first), body1)
	}
	second, body2 := do(t, "GET", base+"/app/x?y=1", nil, "")
	if xc(second) != "HIT" || body2 != body1 {
		t.Fatalf("second: %s %q", xc(second), body2)
	}
	if be.count() != 1 {
		t.Fatalf("backend saw %d requests, want 1", be.count())
	}
	for i, r := range []*http.Response{first, second} {
		if got := r.Header.Values("Server"); len(got) != 1 || got[0] != serverHeader {
			t.Errorf("response %d: Server = %v", i, got)
		}
		if got := r.Header.Values("X-Frame-Options"); len(got) != 1 || got[0] != "DENY" {
			t.Errorf("response %d: X-Frame-Options = %v; the backend must not override the site's headers", i, got)
		}
		if r.Header.Get("Content-Type") != "text/plain" {
			t.Errorf("response %d: Content-Type = %q", i, r.Header.Get("Content-Type"))
		}
	}
	// Another query string is another page.
	if other, _ := do(t, "GET", base+"/app/x?y=2", nil, ""); xc(other) != "MISS" {
		t.Fatalf("another query: %s", xc(other))
	}
	// HEAD is answered from the stored GET, without a body.
	head, hb := do(t, "HEAD", base+"/app/x?y=1", nil, "")
	if xc(head) != "HIT" || hb != "" || head.Header.Get("Content-Length") != fmt.Sprint(len(body1)) {
		t.Fatalf("HEAD: %s %q cl=%s", xc(head), hb, head.Header.Get("Content-Length"))
	}
	if be.count() != 2 {
		t.Fatalf("backend saw %d requests, want 2", be.count())
	}
}

func TestCacheKeepsSitesApart(t *testing.T) {
	be := newCounted(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "for "+r.Host) })
	cfg := cachedConfig(t, be.srv.URL, defaultRouteCache(), testZone())
	b := cfg.Sites[0]
	b.Name = "b.test"
	cfg.Sites = append(cfg.Sites, b)
	h := start(t, cfg)
	base := "http://" + h.addr("http")

	_, a1 := getHost(t, base+"/app/p", "a.test")
	_, b1 := getHost(t, base+"/app/p", "b.test")
	ra, a2 := getHost(t, base+"/app/p", "a.test")
	rb, b2 := getHost(t, base+"/app/p", "b.test")
	if a1 != "for a.test" || b1 != "for b.test" || a2 != a1 || b2 != b1 {
		t.Fatalf("sites share entries: %q %q %q %q", a1, b1, a2, b2)
	}
	if xc(ra) != "HIT" || xc(rb) != "HIT" || be.count() != 2 {
		t.Fatalf("%s %s backend=%d", xc(ra), xc(rb), be.count())
	}
}

func TestCacheNeverStoresWhatItMustNot(t *testing.T) {
	be := newCounted(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/private":
			w.Header().Set("Cache-Control", "private, max-age=600")
		case "/app/nostore":
			w.Header().Set("Cache-Control", "no-store")
		case "/app/cookie":
			w.Header().Set("Set-Cookie", "session=abc; HttpOnly")
		case "/app/vary":
			w.Header().Set("Vary", "*")
		case "/app/error":
			w.WriteHeader(500)
		case "/app/redirect":
			w.Header().Set("Location", "/elsewhere")
			w.WriteHeader(302)
		}
		io.WriteString(w, "body")
	})
	_, base := startCached(t, be.srv.URL, defaultRouteCache())
	for _, p := range []string{"private", "nostore", "cookie", "vary", "error", "redirect"} {
		before := be.count()
		for i := 0; i < 3; i++ {
			resp, _ := do(t, "GET", base+"/app/"+p, nil, "")
			if xc(resp) == "HIT" {
				t.Fatalf("/app/%s was served from the cache", p)
			}
		}
		if got := be.count() - before; got != 3 {
			t.Errorf("/app/%s: backend saw %d of 3 requests", p, got)
		}
	}
}

func TestCacheBypassesRequestsWithCredentials(t *testing.T) {
	be := newCounted(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "user="+r.Header.Get("Cookie")) })
	_, base := startCached(t, be.srv.URL, defaultRouteCache())

	// A logged-in visitor's page must not be shared, and not stored.
	for _, hdr := range []map[string]string{{"Cookie": "session=alice"}, {"Authorization": "Bearer t"}} {
		resp, body := do(t, "GET", base+"/app/me", hdr, "")
		if xc(resp) != "BYPASS" {
			t.Fatalf("%v: X-Cache = %s", hdr, xc(resp))
		}
		if hdr["Cookie"] != "" && body != "user=session=alice" {
			t.Fatalf("body %q", body)
		}
	}
	anon, body := do(t, "GET", base+"/app/me", nil, "")
	if xc(anon) != "MISS" || body != "user=" {
		t.Fatalf("an anonymous visitor got %s %q", xc(anon), body)
	}
	if again, _ := do(t, "GET", base+"/app/me", nil, ""); xc(again) != "HIT" {
		t.Fatalf("anonymous page not cached: %s", xc(again))
	}
	// A request with a cookie still doesn't take the anonymous copy.
	if resp, body := do(t, "GET", base+"/app/me", map[string]string{"Cookie": "session=bob"}, ""); xc(resp) != "BYPASS" || body != "user=session=bob" {
		t.Fatalf("bob got %s %q", xc(resp), body)
	}
}

func TestCacheCookiePolicies(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*model.RouteCache)
		cookie  string
		wantHit bool
	}{
		{"ignore all", func(rc *model.RouteCache) { rc.Cookies = model.CookiesBypassNone }, "a=1", true},
		{"named, other cookie", func(rc *model.RouteCache) {
			rc.Cookies, rc.BypassCookies = model.CookiesBypassNamed, []string{"wordpress_logged_in_*"}
		}, "theme=dark", true},
		{"named, listed cookie", func(rc *model.RouteCache) {
			rc.Cookies, rc.BypassCookies = model.CookiesBypassNamed, []string{"wordpress_logged_in_*"}
		}, "wordpress_logged_in_x=1", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			be := newCounted(t, page200("x"))
			rc := defaultRouteCache()
			c.mutate(&rc)
			_, base := startCached(t, be.srv.URL, rc)
			do(t, "GET", base+"/app/k", nil, "")
			resp, _ := do(t, "GET", base+"/app/k", map[string]string{"Cookie": c.cookie}, "")
			if (xc(resp) == "HIT") != c.wantHit {
				t.Fatalf("X-Cache = %s", xc(resp))
			}
		})
	}
}

func TestCacheWritesInvalidate(t *testing.T) {
	var version atomic.Int32
	version.Store(1)
	be := newCounted(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			version.Add(1)
			w.WriteHeader(201)
			return
		}
		fmt.Fprintf(w, "v%d", version.Load())
	})
	_, base := startCached(t, be.srv.URL, defaultRouteCache())
	if _, body := do(t, "GET", base+"/app/items", nil, ""); body != "v1" {
		t.Fatal(body)
	}
	if _, body := do(t, "GET", base+"/app/items?page=2", nil, ""); body != "v1" {
		t.Fatal(body)
	}
	if resp, _ := do(t, "POST", base+"/app/items", nil, "new"); resp.StatusCode != 201 {
		t.Fatalf("POST: %d", resp.StatusCode)
	}
	for _, u := range []string{"/app/items", "/app/items?page=2"} {
		resp, body := do(t, "GET", base+u, nil, "")
		if body != "v2" || xc(resp) != "MISS" {
			t.Fatalf("%s after a POST: %s %q; the stored copy is stale", u, xc(resp), body)
		}
	}
}

func TestCacheExpiresAndServesStaleOnBackendErrors(t *testing.T) {
	var mode atomic.Int32 // 0 ok, 1 error 503
	be := newCounted(t, func(w http.ResponseWriter, r *http.Request) {
		if mode.Load() == 1 {
			w.WriteHeader(503)
			io.WriteString(w, "maintenance")
			return
		}
		io.WriteString(w, "fresh content")
	})
	rc := defaultRouteCache()
	rc.TTL = model.Duration(10 * time.Second)
	rc.StaleIfError = model.Duration(time.Hour)
	h, base := startCached(t, be.srv.URL, rc)
	clock := newClock()
	zoneStore(h, "pages").SetClock(clock.now)

	do(t, "GET", base+"/app/s", nil, "")
	clock.advance(5 * time.Second)
	if resp, _ := do(t, "GET", base+"/app/s", nil, ""); xc(resp) != "HIT" {
		t.Fatalf("within the lifetime: %s", xc(resp))
	}
	clock.advance(10 * time.Second) // past the lifetime
	mode.Store(1)
	resp, body := do(t, "GET", base+"/app/s", nil, "")
	if resp.StatusCode != 200 || body != "fresh content" || xc(resp) != "STALE" {
		t.Fatalf("a 503 from the backend: %d %q %s", resp.StatusCode, body, xc(resp))
	}
	// The backend disappears altogether: the proxy can't connect.
	be.srv.Close()
	resp, body = do(t, "GET", base+"/app/s", nil, "")
	if resp.StatusCode != 200 || body != "fresh content" || xc(resp) != "STALE" {
		t.Fatalf("backend down: %d %q %s", resp.StatusCode, body, xc(resp))
	}
	if !strings.Contains(h.stderr.String(), "served a stale cached response") {
		t.Errorf("serving stale was not logged:\n%s", h.stderr.String())
	}
	// Past the stale window the error is the answer.
	clock.advance(2 * time.Hour)
	if resp, _ := do(t, "GET", base+"/app/s", nil, ""); resp.StatusCode != 502 && resp.StatusCode != 503 {
		t.Fatalf("after the stale window: %d", resp.StatusCode)
	}
	// A page that was never cached gets the proxy's own error.
	if resp, _ := do(t, "GET", base+"/app/never", nil, ""); resp.StatusCode != 502 && resp.StatusCode != 503 {
		t.Fatalf("uncached page, backend down: %d", resp.StatusCode)
	}
}

func TestCacheIsHitEvenWhenTheBackendIsDown(t *testing.T) {
	be := newCounted(t, page200("hello"))
	_, base := startCached(t, be.srv.URL, defaultRouteCache())
	do(t, "GET", base+"/app/up", nil, "")
	be.srv.Close()
	resp, body := do(t, "GET", base+"/app/up", nil, "")
	if resp.StatusCode != 200 || body != "hello" || xc(resp) != "HIT" {
		t.Fatalf("%d %q %s", resp.StatusCode, body, xc(resp))
	}
}

func TestCacheCoalescesSimultaneousMisses(t *testing.T) {
	be := newCounted(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		io.WriteString(w, "slow page")
	})
	_, base := startCached(t, be.srv.URL, defaultRouteCache())
	const n = 12
	var wg sync.WaitGroup
	var bad atomic.Int32
	var hits atomic.Int32
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, body := do(t, "GET", base+"/app/herd", nil, "")
			if resp.StatusCode != 200 || body != "slow page" {
				bad.Add(1)
			}
			if xc(resp) == "HIT" {
				hits.Add(1)
			}
		}()
	}
	wg.Wait()
	if bad.Load() != 0 {
		t.Fatalf("%d bad responses", bad.Load())
	}
	if be.count() != 1 {
		t.Fatalf("backend saw %d requests for %d simultaneous ones", be.count(), n)
	}
	if hits.Load() != n-1 {
		t.Fatalf("%d hits, want %d", hits.Load(), n-1)
	}
}

func TestCacheLargeResponsesPassThrough(t *testing.T) {
	big := strings.Repeat("0123456789abcdef", 160<<10) // 2.5 MiB, over max_entry_size
	be := newCounted(t, page200(big))
	_, base := startCached(t, be.srv.URL, defaultRouteCache())
	for i := 0; i < 2; i++ {
		resp, body := do(t, "GET", base+"/app/big", nil, "")
		if body != big || xc(resp) != "MISS" {
			t.Fatalf("round %d: %s, %d bytes", i, xc(resp), len(body))
		}
	}
	if be.count() != 2 {
		t.Fatalf("backend saw %d requests", be.count())
	}
}

func TestCacheRangeAndConditionalRequestsOnAHit(t *testing.T) {
	be := newCounted(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"abc"`)
		io.WriteString(w, "0123456789")
	})
	_, base := startCached(t, be.srv.URL, defaultRouteCache())
	do(t, "GET", base+"/app/r", nil, "")
	resp, body := do(t, "GET", base+"/app/r", map[string]string{"Range": "bytes=3-5"}, "")
	if resp.StatusCode != 206 || body != "345" {
		t.Fatalf("range: %d %q", resp.StatusCode, body)
	}
	resp, body = do(t, "GET", base+"/app/r", map[string]string{"If-None-Match": `"abc"`}, "")
	if resp.StatusCode != 304 || body != "" {
		t.Fatalf("conditional: %d %q", resp.StatusCode, body)
	}
	if be.count() != 1 {
		t.Fatalf("backend saw %d requests", be.count())
	}
}

func TestCacheVaryOnAcceptEncoding(t *testing.T) {
	be := newCounted(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "Accept-Language")
		fmt.Fprint(w, "lang="+r.Header.Get("Accept-Language"))
	})
	_, base := startCached(t, be.srv.URL, defaultRouteCache())
	get := func(lang string) (*http.Response, string) {
		return do(t, "GET", base+"/app/v", map[string]string{"Accept-Language": lang}, "")
	}
	if _, b := get("de"); b != "lang=de" {
		t.Fatal(b)
	}
	if r, b := get("fr"); b != "lang=fr" || xc(r) != "MISS" {
		t.Fatalf("a different language got %s %q", xc(r), b)
	}
	if r, b := get("de"); b != "lang=de" || xc(r) != "HIT" {
		t.Fatalf("first variant lost: %s %q", xc(r), b)
	}
}

func TestCacheEntriesSurviveAReload(t *testing.T) {
	be := newCounted(t, page200("kept"))
	cfg := cachedConfig(t, be.srv.URL, defaultRouteCache(), testZone())
	h := start(t, cfg)
	base := "http://" + h.addr("http")
	do(t, "GET", base+"/app/a", nil, "")
	do(t, "GET", base+"/app/b", nil, "")

	reload := func(rc model.RouteCache, zone model.CacheZone) {
		t.Helper()
		next := cachedConfig(t, be.srv.URL, rc, zone)
		next.Sites[0].Root = cfg.Sites[0].Root
		next.Listeners = cfg.Listeners
		if err := h.srv.Reload(next); err != nil {
			t.Fatal(err)
		}
	}
	reload(defaultRouteCache(), testZone())
	if resp, _ := do(t, "GET", base+"/app/a", nil, ""); xc(resp) != "HIT" {
		t.Fatalf("after a reload: %s", xc(resp))
	}

	// Shrinking the zone drops entries at once.
	small := testZone()
	small.MaxSize = 1 << 20
	for i := 0; i < 40; i++ {
		do(t, "GET", base+fmt.Sprintf("/app/fill%d", i), nil, "")
	}
	before, _ := h.srv.Caches("pages")
	tiny := small
	tiny.MaxSize, tiny.MaxEntrySize = 3000, 1000
	reload(defaultRouteCache(), tiny)
	after, _ := h.srv.Caches("pages")
	if after[0].Bytes > 3000 || after[0].Entries >= before[0].Entries || after[0].MaxBytes != 3000 {
		t.Fatalf("before %+v, after %+v", before[0], after[0])
	}

	// A zone that is gone from the configuration is gone.
	plain := routedConfig(t, nil, []model.Route{proxyRoute("/app/", model.Actions{Proxy: "app"})})
	plain.Upstreams = []model.Upstream{upstreamOf("app", be.srv.URL)}
	plain.Sites[0].Root = cfg.Sites[0].Root
	plain.Listeners = cfg.Listeners
	if err := h.srv.Reload(plain); err != nil {
		t.Fatal(err)
	}
	if infos, _ := h.srv.Caches(""); len(infos) != 0 {
		t.Fatalf("zones left: %+v", infos)
	}
	if resp, _ := do(t, "GET", base+"/app/a", nil, ""); xc(resp) != "" {
		t.Fatalf("route without cache sets X-Cache: %q", xc(resp))
	}
}

func TestCacheStatusAndPurge(t *testing.T) {
	be := newCounted(t, page200("x"))
	cfg := cachedConfig(t, be.srv.URL, defaultRouteCache(), testZone())
	b := cfg.Sites[0]
	b.Name = "b.test"
	cfg.Sites = append(cfg.Sites, b)
	h := start(t, cfg)
	base := "http://" + h.addr("http")
	fill := func() {
		for _, host := range []string{"a.test", "b.test"} {
			for _, p := range []string{"/app/news/1", "/app/news/2", "/app/other"} {
				getHost(t, base+p, host)
			}
		}
	}
	fill()
	infos, err := h.srv.Caches("")
	if err != nil || len(infos) != 1 || infos[0].Zone != "pages" || infos[0].Entries != 6 || infos[0].Misses != 6 {
		t.Fatalf("%+v %v", infos, err)
	}
	getHost(t, base+"/app/other", "a.test")
	infos, _ = h.srv.Caches("pages")
	if infos[0].Hits != 1 {
		t.Fatalf("hits = %d", infos[0].Hits)
	}
	if _, err := h.srv.Caches("nope"); err == nil {
		t.Fatal("unknown zone accepted")
	}

	steps := []struct {
		m    cache.Match
		want int
	}{
		{cache.Match{Host: "a.test", Path: "/app/other"}, 1},
		{cache.Match{Prefix: "/app/news/"}, 4},
		{cache.Match{All: true}, 1},
	}
	for _, s := range steps {
		if got, err := h.srv.PurgeCache("pages", s.m); err != nil || got != s.want {
			t.Fatalf("purge %+v: %d %v, want %d", s.m, got, err, s.want)
		}
	}
	fill()
	for name, m := range map[string]cache.Match{
		"nothing":       {},
		"all and path":  {All: true, Path: "/app/x"},
		"path+prefix":   {Path: "/a", Prefix: "/b"},
		"relative path": {Path: "app/x"},
		"host only":     {Host: "a.test"},
	} {
		if _, err := h.srv.PurgeCache("pages", m); err == nil {
			t.Errorf("%s: the purge was accepted", name)
		}
	}
	if got, _ := h.srv.Caches("pages"); got[0].Entries != 6 {
		t.Fatalf("a rejected purge removed entries: %+v", got[0])
	}
	if _, err := h.srv.PurgeCache("nope", cache.Match{All: true}); err == nil {
		t.Fatal("unknown zone accepted")
	}
	// After a purge the page is fetched again.
	h.srv.PurgeCache("pages", cache.Match{All: true})
	before := be.count()
	if resp, _ := do(t, "GET", base+"/app/news/1", nil, ""); xc(resp) != "MISS" || be.count() != before+1 {
		t.Fatalf("after a purge: %s", xc(resp))
	}
}

func TestCacheMetrics(t *testing.T) {
	be := newCounted(t, page200("m"))
	cfg := withMetrics(cachedConfig(t, be.srv.URL, defaultRouteCache(), testZone()))
	h := start(t, cfg)
	base := "http://" + h.addr("http")
	do(t, "GET", base+"/app/m", nil, "")                                // miss
	do(t, "GET", base+"/app/m", nil, "")                                // hit
	do(t, "GET", base+"/app/m", nil, "")                                // hit
	do(t, "GET", base+"/app/m", map[string]string{"Cookie": "s=1"}, "") // bypass
	do(t, "GET", base+"/app/m2", nil, "")                               // miss

	fams, _ := scrape(t, h, nil)
	for _, c := range []struct {
		result string
		want   float64
	}{{"hit", 2}, {"miss", 2}, {"bypass", 1}, {"stale", 0}} {
		if v, ok := fams.value(t, "lotus_cache_requests_total", map[string]string{"zone": "pages", "result": c.result}); !ok || v != c.want {
			t.Errorf("lotus_cache_requests_total{result=%q} = %v (found %v), want %v", c.result, v, ok, c.want)
		}
	}
	if v, ok := fams.value(t, "lotus_cache_entries", map[string]string{"zone": "pages"}); !ok || v != 2 {
		t.Errorf("entries = %v", v)
	}
	if v, ok := fams.value(t, "lotus_cache_size_bytes", map[string]string{"zone": "pages"}); !ok || v <= 0 {
		t.Errorf("size = %v", v)
	}
	if v, ok := fams.value(t, "lotus_cache_max_size_bytes", map[string]string{"zone": "pages"}); !ok || v != float64(8<<20) {
		t.Errorf("max size = %v", v)
	}
	if v, ok := fams.value(t, "lotus_cache_stored_total", map[string]string{"zone": "pages"}); !ok || v != 2 {
		t.Errorf("stored = %v", v)
	}
}

func TestCacheHidesNothingFromErrorPagesAndStatus(t *testing.T) {
	// A cached 404 from the backend is the backend's page, as without a cache.
	be := newCounted(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=30")
		w.WriteHeader(404)
		io.WriteString(w, "backend 404 page")
	})
	cfg := cachedConfig(t, be.srv.URL, defaultRouteCache(), testZone())
	cfg.Sites[0].Routes[0].ErrorPages = map[int]string{404: "/404.html"}
	cfg.Sites[0].ErrorPages = map[int]string{404: "/404.html"}
	h := start(t, cfg)
	base := "http://" + h.addr("http")
	for i, want := range []string{"MISS", "HIT"} {
		resp, body := do(t, "GET", base+"/app/gone", nil, "")
		if resp.StatusCode != 404 || body != "backend 404 page" || xc(resp) != want {
			t.Fatalf("round %d: %d %q %s", i, resp.StatusCode, body, xc(resp))
		}
	}
}

func TestCacheKeyFollowsTheRequestedURL(t *testing.T) {
	store := cache.New(1<<20, 1<<10)
	rt := &siteRuntime{caches: map[string]*cache.Store{"pages": store}}
	route := &model.Route{Cache: &model.RouteCache{Zone: "pages", Cookies: model.CookiesBypassAny}}
	mk := func(rawurl, host string, tls bool) *http.Request {
		r := httptest.NewRequest("GET", rawurl, nil)
		r.Host = host
		if tls {
			r.TLS = &tls0
		}
		return r
	}
	cases := []struct {
		name string
		r    *http.Request
		want cache.Key
	}{
		{"plain", mk("/a/b?x=1", "Example.COM", false), cache.Key{Host: "example.com", Path: "/a/b", Query: "x=1", Scheme: "http"}},
		{"port and dot are not part of the host", mk("/a", "example.com.:8080", false), cache.Key{Host: "example.com", Path: "/a", Scheme: "http"}},
		{"tls", mk("/a", "example.com", true), cache.Key{Host: "example.com", Path: "/a", Scheme: "https"}},
		{"escaped path stays escaped", mk("/a%20b/c%2Fd", "example.com", false), cache.Key{Host: "example.com", Path: "/a%20b/c%2Fd", Scheme: "http"}},
		{"query is raw", mk("/a?b=%41&c", "example.com", false), cache.Key{Host: "example.com", Path: "/a", Query: "b=%41&c", Scheme: "http"}},
	}
	for _, c := range cases {
		scheme := "http"
		if c.r.TLS != nil {
			scheme = "https"
		}
		cc := rt.cacheFor(route, c.r, scheme)
		if cc == nil || cc.key != c.want || cc.store != store {
			t.Errorf("%s: %+v, want %+v", c.name, cc, c.want)
		}
	}
	if cc := rt.cacheFor(&model.Route{}, mk("/", "example.com", false), "http"); cc != nil {
		t.Error("a route without a cache got one")
	}
	if cc := rt.cacheFor(nil, mk("/", "example.com", false), "http"); cc != nil {
		t.Error("no route, a cache")
	}
	if cc := rt.cacheFor(&model.Route{Cache: &model.RouteCache{Zone: "gone"}}, mk("/", "example.com", false), "http"); cc != nil {
		t.Error("a zone that doesn't exist must not panic or cache")
	}
	if cc := rt.cacheFor(route, mk("/", "bad host", false), "http"); cc != nil {
		t.Error("an invalid host must not be cached under")
	}
}

func TestCacheOptionsFromTheConfiguration(t *testing.T) {
	o := cacheOptions(&model.RouteCache{
		TTL: model.Duration(time.Minute), NegativeTTL: model.Duration(3 * time.Second),
		StaleIfError: model.Duration(time.Hour), LockTimeout: model.Duration(7 * time.Second), Cookies: model.CookiesBypassAny,
	})
	if o.TTL != time.Minute || o.NegativeTTL != 3*time.Second || o.StaleIfError != time.Hour || o.LockTimeout != 7*time.Second || o.Cookies != cache.CookiesAny {
		t.Fatalf("%+v", o)
	}
	if o := cacheOptions(&model.RouteCache{Cookies: model.CookiesBypassNone}); o.Cookies != cache.CookiesIgnore {
		t.Fatalf("%+v", o)
	}
	o = cacheOptions(&model.RouteCache{Cookies: model.CookiesBypassNamed, BypassCookies: []string{"a", "b*"}})
	if o.Cookies != cache.CookiesNamed || len(o.CookieNames) != 2 || o.CookieNames[1] != "b*" {
		t.Fatalf("%+v", o)
	}
}

// ---- PHP ----

// startCachedPHP is startPHP with a cache zone and a cache on the script route.
func startCachedPHP(t *testing.T, handler func(*fcgitest.Req), rc model.RouteCache) *phpTest {
	t.Helper()
	pt := &phpTest{calls: &calls{}}
	pt.be = fcgitest.New(t, func(r *fcgitest.Req) {
		pt.calls.add(r.Params, nil)
		handler(r)
	})
	re := `\.php(/|$)`
	routes := []model.Route{
		{Kind: model.RouteRegex, Path: re, Regexp: mustRe(re), Actions: model.Actions{PHP: "app"}, Cache: &rc},
		{Kind: model.RoutePrefix, Path: "/", Actions: model.Actions{TryFiles: []string{"{uri}", "{uri}/", "/index.php?{query}"}}},
	}
	cfg := routedConfig(t, func(s *model.Site) {
		s.Index = []string{"index.php", "index.html"}
		writePHPFiles(t, s.Root)
	}, routes)
	cfg.PHPBackends = []model.PHPBackend{{Name: "app", Network: "tcp", Address: pt.be.Addr(), Timeout: model.Duration(3 * time.Second)}}
	cfg.Caches = []model.CacheZone{testZone()}
	pt.cfg, pt.root = cfg, cfg.Sites[0].Root
	pt.h = start(t, cfg)
	pt.base = "http://" + pt.h.addr("http")
	return pt
}

func TestCacheForPHPKeysOnTheRequestedURL(t *testing.T) {
	p := startCachedPHP(t, func(r *fcgitest.Req) {
		r.Respond("Content-Type: text/html", "page for "+r.Params["REQUEST_URI"])
	}, defaultRouteCache())

	first, b1 := p.get(t, "/pretty/url?x=1")
	if first.StatusCode != 200 || b1 != "page for /pretty/url?x=1" || xc(first) != "MISS" {
		t.Fatalf("%d %q %s", first.StatusCode, b1, xc(first))
	}
	second, b2 := p.get(t, "/pretty/url?x=1")
	if b2 != b1 || xc(second) != "HIT" || p.calls.count() != 1 {
		t.Fatalf("%q %s calls=%d", b2, xc(second), p.calls.count())
	}
	// Both of these run /index.php?x=1 after routing, and must not share a page:
	// the key is the URL the client asked for.
	if other, body := p.get(t, "/pretty/other?x=1"); body != "page for /pretty/other?x=1" || xc(other) != "MISS" {
		t.Fatalf("%q %s", body, xc(other))
	}
	if p.calls.count() != 2 {
		t.Fatalf("PHP ran %d times", p.calls.count())
	}
	// Static files on the same site are not cached.
	if resp, body := p.get(t, "/style.css"); body != "body{}" || xc(resp) != "" {
		t.Fatalf("static file: %q X-Cache=%q", body, xc(resp))
	}
}

func TestCacheForPHPSkipsSessionsAndCookies(t *testing.T) {
	p := startCachedPHP(t, func(r *fcgitest.Req) {
		switch r.Params["QUERY_STRING"] {
		case "session":
			r.Respond("Content-Type: text/html\r\nSet-Cookie: PHPSESSID=abc; path=/\r\nCache-Control: no-store, no-cache, must-revalidate\r\nExpires: Thu, 19 Nov 1981 08:52:00 GMT", "private page")
		default:
			r.Respond("Content-Type: text/html", "public page "+r.Params["HTTP_COOKIE"])
		}
	}, defaultRouteCache())

	for i := 0; i < 3; i++ {
		if resp, _ := p.get(t, "/app.php?session"); xc(resp) == "HIT" {
			t.Fatal("a response that starts a session was served from the cache")
		}
	}
	if p.calls.count() != 3 {
		t.Fatalf("PHP ran %d times for 3 session requests", p.calls.count())
	}
	p.get(t, "/app.php?public")
	if resp, body := do(t, "GET", p.base+"/app.php?public", map[string]string{"Cookie": "PHPSESSID=zzz"}, ""); xc(resp) != "BYPASS" || body != "public page PHPSESSID=zzz" {
		t.Fatalf("%s %q", xc(resp), body)
	}
}

func TestCacheForPHPDoesNotStoreFailures(t *testing.T) {
	var fail atomic.Bool
	p := startCachedPHP(t, func(r *fcgitest.Req) {
		if fail.Load() {
			r.Respond("Status: 500 Internal Server Error\r\nContent-Type: text/html", "broken")
			return
		}
		r.Respond("Content-Type: text/html", "fine")
	}, defaultRouteCache())
	fail.Store(true)
	for i := 0; i < 2; i++ {
		if resp, body := p.get(t, "/app.php"); resp.StatusCode != 500 || body != "broken" || xc(resp) == "HIT" {
			t.Fatalf("%d %q %s", resp.StatusCode, body, xc(resp))
		}
	}
	fail.Store(false)
	if resp, body := p.get(t, "/app.php"); resp.StatusCode != 200 || body != "fine" || xc(resp) != "MISS" {
		t.Fatalf("%d %q %s", resp.StatusCode, body, xc(resp))
	}
	if resp, _ := p.get(t, "/app.php"); xc(resp) != "HIT" {
		t.Fatalf("%s", xc(resp))
	}
}
