package cache

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type env struct {
	t     *testing.T
	s     *Store
	now   atomic.Int64
	calls atomic.Int32 // how often a backend handler ran
}

func newEnv(t *testing.T, max, maxEntry int64) *env {
	t.Helper()
	e := &env{t: t, s: New(max, maxEntry)}
	e.now.Store(time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC).UnixNano())
	e.s.SetClock(func() time.Time { return time.Unix(0, e.now.Load()).UTC() })
	return e
}

func (e *env) advance(d time.Duration) { e.now.Add(int64(d)) }

func defaults() Options { return Options{TTL: time.Minute, LockTimeout: 2 * time.Second} }

func keyOf(r *http.Request) Key {
	scheme := "https"
	return Key{Host: "example.com", Path: r.URL.EscapedPath(), Query: r.URL.RawQuery, Scheme: scheme}
}

// do runs a request through the cache; h plays the backend.
func (e *env) do(method, target string, o Options, mod func(*http.Request), h http.HandlerFunc) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if mod != nil {
		mod(req)
	}
	return e.doReq(req, o, h)
}

func (e *env) doReq(req *http.Request, o Options, h http.HandlerFunc) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.s.Serve(rec, req, keyOf(req), o, func(w http.ResponseWriter) {
		e.calls.Add(1)
		h(w, req)
	})
	return rec
}

func page(body string, kv ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i+1 < len(kv); i += 2 {
			w.Header().Add(kv[i], kv[i+1])
		}
		_, _ = io.WriteString(w, body)
	}
}

func xcache(rec *httptest.ResponseRecorder) string { return rec.Header().Get("X-Cache") }

func TestMissThenHit(t *testing.T) {
	e := newEnv(t, 1<<20, 1<<16)
	h := page("hello", "Content-Type", "text/plain", "X-Backend", "yes")
	first := e.do("GET", "/a", defaults(), nil, h)
	if xcache(first) != "MISS" || first.Body.String() != "hello" {
		t.Fatalf("first: %q %q", xcache(first), first.Body.String())
	}
	e.advance(3 * time.Second)
	second := e.do("GET", "/a", defaults(), nil, h)
	if xcache(second) != "HIT" || second.Body.String() != "hello" {
		t.Fatalf("second: %q %q", xcache(second), second.Body.String())
	}
	if e.calls.Load() != 1 {
		t.Fatalf("backend ran %d times, want 1", e.calls.Load())
	}
	if second.Header().Get("X-Backend") != "yes" || second.Header().Get("Content-Type") != "text/plain" {
		t.Fatalf("headers lost: %v", second.Header())
	}
	if second.Header().Get("Age") != "3" {
		t.Fatalf("Age = %q, want 3", second.Header().Get("Age"))
	}
	if second.Header().Get("Content-Length") != "5" {
		t.Fatalf("Content-Length = %q", second.Header().Get("Content-Length"))
	}
	st := e.s.Stats()
	if st.Hits != 1 || st.Misses != 1 || st.Stored != 1 || st.Entries != 1 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestLifetimeRules(t *testing.T) {
	future := func(d time.Duration) string {
		return time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC).Add(d).Format(http.TimeFormat)
	}
	cases := []struct {
		name   string
		opts   Options
		hdr    []string
		status int
		fresh  time.Duration // 0: not stored
	}{
		{"default ttl", Options{TTL: 30 * time.Second}, nil, 200, 30 * time.Second},
		{"no ttl and no headers", Options{}, nil, 200, 0},
		{"max-age beats ttl", Options{TTL: time.Hour}, []string{"Cache-Control", "max-age=10"}, 200, 10 * time.Second},
		{"s-maxage beats max-age", Options{}, []string{"Cache-Control", "max-age=10, s-maxage=50"}, 200, 50 * time.Second},
		{"max-age 0", Options{TTL: time.Hour}, []string{"Cache-Control", "max-age=0"}, 200, 0},
		{"no-store", Options{TTL: time.Hour}, []string{"Cache-Control", "no-store"}, 200, 0},
		{"private", Options{TTL: time.Hour}, []string{"Cache-Control", "private, max-age=60"}, 200, 0},
		{"no-cache", Options{TTL: time.Hour}, []string{"Cache-Control", "no-cache"}, 200, 0},
		{"public", Options{}, []string{"Cache-Control", "public, max-age=20"}, 200, 20 * time.Second},
		{"conflicting max-age on one line", Options{}, []string{"Cache-Control", "max-age=100, max-age=10"}, 200, 10 * time.Second},
		{"conflicting max-age on two lines", Options{}, []string{"Cache-Control", "max-age=10", "Cache-Control", "max-age=100"}, 200, 10 * time.Second},
		{"quoted max-age", Options{}, []string{"Cache-Control", `max-age="25"`}, 200, 25 * time.Second},
		{"a comma in a quoted string", Options{}, []string{"Cache-Control", `max-age=25, ext="a,b"`}, 200, 25 * time.Second},
		{"garbage max-age", Options{TTL: time.Hour}, []string{"Cache-Control", "max-age=abc"}, 200, 0},
		{"negative max-age", Options{TTL: time.Hour}, []string{"Cache-Control", "max-age=-5"}, 200, 0},
		{"no-cache with field names is still not kept", Options{TTL: time.Hour}, []string{"Cache-Control", `no-cache="Set-Cookie"`}, 200, 0},
		{"directive names are case-insensitive", Options{}, []string{"Cache-Control", "MAX-AGE=12"}, 200, 12 * time.Second},
		{"private on a second line", Options{TTL: time.Hour}, []string{"Cache-Control", "max-age=60", "Cache-Control", "private"}, 200, 0},
		{"set-cookie", Options{TTL: time.Hour}, []string{"Set-Cookie", "a=b"}, 200, 0},
		{"expires", Options{}, []string{"Expires", future(40 * time.Second)}, 200, 40 * time.Second},
		{"expires in the past", Options{TTL: time.Hour}, []string{"Expires", future(-time.Hour)}, 200, 0},
		{"expires 0", Options{TTL: time.Hour}, []string{"Expires", "0"}, 200, 0},
		{"age is subtracted", Options{}, []string{"Cache-Control", "max-age=60", "Age", "45"}, 200, 15 * time.Second},
		{"already too old", Options{}, []string{"Cache-Control", "max-age=60", "Age", "70"}, 200, 0},
		{"pragma no-cache", Options{TTL: time.Hour}, []string{"Pragma", "no-cache"}, 200, 0},
		{"vary star", Options{TTL: time.Hour}, []string{"Vary", "*"}, 200, 0},
		{"trailer", Options{TTL: time.Hour}, []string{"Trailer", "X-Sum"}, 200, 0},
		{"event stream", Options{TTL: time.Hour}, []string{"Content-Type", "text/event-stream"}, 200, 0},
		{"404 default is not kept", Options{TTL: time.Hour}, nil, 404, 0},
		{"404 negative ttl", Options{NegativeTTL: 7 * time.Second}, nil, 404, 7 * time.Second},
		{"404 explicit", Options{}, []string{"Cache-Control", "max-age=9"}, 404, 9 * time.Second},
		{"410", Options{NegativeTTL: 7 * time.Second}, nil, 410, 7 * time.Second},
		{"301", Options{TTL: 5 * time.Second}, []string{"Location", "/x"}, 301, 5 * time.Second},
		{"500 never", Options{TTL: time.Hour}, nil, 500, 0},
		{"302 never", Options{TTL: time.Hour}, []string{"Location", "/x"}, 302, 0},
		{"206 never", Options{TTL: time.Hour}, nil, 206, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, 1<<20, 1<<16)
			o := c.opts
			o.LockTimeout = time.Second
			h := func(w http.ResponseWriter, r *http.Request) {
				for i := 0; i+1 < len(c.hdr); i += 2 {
					w.Header().Add(c.hdr[i], c.hdr[i+1])
				}
				w.WriteHeader(c.status)
				_, _ = io.WriteString(w, "body")
			}
			e.do("GET", "/p", o, nil, h)
			if c.fresh == 0 {
				if e.s.Stats().Entries != 0 {
					t.Fatalf("stored, want not stored")
				}
				return
			}
			if e.s.Stats().Entries != 1 {
				t.Fatalf("not stored")
			}
			e.advance(c.fresh - time.Second)
			if got := xcache(e.do("GET", "/p", o, nil, h)); got != "HIT" {
				t.Fatalf("1s before the end: %s", got)
			}
			e.advance(time.Second)
			if got := xcache(e.do("GET", "/p", o, nil, h)); got != "MISS" {
				t.Fatalf("at the end: %s, want MISS", got)
			}
		})
	}
}

func TestKeysAreSeparate(t *testing.T) {
	e := newEnv(t, 1<<20, 1<<16)
	n := 0
	h := func(w http.ResponseWriter, r *http.Request) {
		n++
		fmt.Fprintf(w, "%d", n)
	}
	a := e.do("GET", "/x?a=1", defaults(), nil, h).Body.String()
	b := e.do("GET", "/x?a=2", defaults(), nil, h).Body.String()
	c := e.do("GET", "/x", defaults(), nil, h).Body.String()
	if a == b || b == c || a == c {
		t.Fatalf("queries share an entry: %s %s %s", a, b, c)
	}
	if got := e.do("GET", "/x?a=1", defaults(), nil, h).Body.String(); got != a {
		t.Fatalf("same query not served from the cache: %s vs %s", got, a)
	}
	// Another host and another scheme are other entries.
	req := httptest.NewRequest("GET", "/x?a=1", nil)
	other := Key{Host: "other.test", Path: "/x", Query: "a=1", Scheme: "https"}
	rec := httptest.NewRecorder()
	e.s.Serve(rec, req, other, defaults(), func(w http.ResponseWriter) { h(w, req) })
	if xcache(rec) != "MISS" {
		t.Fatalf("another host shares the entry")
	}
	plain := Key{Host: "example.com", Path: "/x", Query: "a=1", Scheme: "http"}
	rec = httptest.NewRecorder()
	e.s.Serve(rec, req, plain, defaults(), func(w http.ResponseWriter) { h(w, req) })
	if xcache(rec) != "MISS" {
		t.Fatalf("another scheme shares the entry")
	}
}

func TestVary(t *testing.T) {
	e := newEnv(t, 1<<20, 1<<16)
	h := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "Accept-Encoding, X-Lang")
		fmt.Fprintf(w, "%s|%s", r.Header.Get("Accept-Encoding"), r.Header.Get("X-Lang"))
	}
	get := func(enc, lang string) *httptest.ResponseRecorder {
		return e.do("GET", "/v", defaults(), func(r *http.Request) {
			if enc != "" {
				r.Header.Set("Accept-Encoding", enc)
			}
			if lang != "" {
				r.Header.Set("X-Lang", lang)
			}
		}, h)
	}
	if xcache(get("gzip", "en")) != "MISS" {
		t.Fatal("first")
	}
	if xcache(get("gzip", "en")) != "HIT" {
		t.Fatal("same headers must hit")
	}
	if got := get("gzip", "de"); xcache(got) != "MISS" || got.Body.String() != "gzip|de" {
		t.Fatalf("another language: %s %q", xcache(got), got.Body.String())
	}
	if got := get("br", "en"); xcache(got) != "MISS" || got.Body.String() != "br|en" {
		t.Fatalf("another encoding: %s %q", xcache(got), got.Body.String())
	}
	if got := get("gzip", "en"); xcache(got) != "HIT" || got.Body.String() != "gzip|en" {
		t.Fatalf("first variant lost: %s %q", xcache(got), got.Body.String())
	}
	if got := get("", "en"); xcache(got) != "MISS" {
		t.Fatalf("no encoding is a variant of its own: %s", xcache(got))
	}
	// The same set of encodings in another order or with weights is the same variant.
	get("gzip, br", "en")
	if got := get("br;q=1.0, gzip;q=0.5", "en"); xcache(got) != "HIT" {
		t.Fatalf("equivalent Accept-Encoding: %s", xcache(got))
	}
	if got := get("br;q=1.0, gzip;q=0", "en"); xcache(got) != "HIT" || got.Body.String() != "br|en" {
		t.Fatalf("a coding with q=0 must not count: %s %q", xcache(got), got.Body.String())
	}
	if e.s.Stats().Entries < 4 {
		t.Fatalf("variants not kept: %+v", e.s.Stats())
	}
}

func TestVariantLimit(t *testing.T) {
	e := newEnv(t, 1<<20, 1<<16)
	h := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "X-Id")
		io.WriteString(w, r.Header.Get("X-Id"))
	}
	for i := 0; i < 3*maxVariants; i++ {
		e.advance(time.Second)
		e.do("GET", "/limit", defaults(), func(r *http.Request) { r.Header.Set("X-Id", fmt.Sprint(i)) }, h)
	}
	if n := e.s.Stats().Entries; n != maxVariants {
		t.Fatalf("%d variants kept, want %d", n, maxVariants)
	}
	// The newest ones are the ones kept.
	last := fmt.Sprint(3*maxVariants - 1)
	rec := e.do("GET", "/limit", defaults(), func(r *http.Request) { r.Header.Set("X-Id", last) }, h)
	if xcache(rec) != "HIT" {
		t.Fatalf("newest variant evicted")
	}
	old := e.do("GET", "/limit", defaults(), func(r *http.Request) { r.Header.Set("X-Id", "0") }, h)
	if xcache(old) != "MISS" {
		t.Fatalf("oldest variant kept")
	}
}

func TestVaryOnTooManyHeadersIsNotStored(t *testing.T) {
	e := newEnv(t, 1<<20, 1<<16)
	h := page("x", "Vary", "A, B, C, D, E, F, G")
	e.do("GET", "/many", defaults(), nil, h)
	if e.s.Stats().Entries != 0 {
		t.Fatal("stored a response that varies on 7 headers")
	}
}

func TestRequestsThatBypass(t *testing.T) {
	cases := []struct {
		name string
		o    Options
		mod  func(*http.Request)
		want string // X-Cache of the second request
	}{
		{"cookie by default", defaults(), func(r *http.Request) { r.Header.Set("Cookie", "a=b") }, "BYPASS"},
		{"authorization", defaults(), func(r *http.Request) { r.Header.Set("Authorization", "Basic eDp5") }, "BYPASS"},
		{"upgrade", defaults(), func(r *http.Request) { r.Header.Set("Upgrade", "websocket") }, "BYPASS"},
		{"get with body", defaults(), func(r *http.Request) { r.ContentLength = 3; r.Body = io.NopCloser(strings.NewReader("abc")) }, "BYPASS"},
		{"cookies ignored", Options{TTL: time.Minute, LockTimeout: time.Second, Cookies: CookiesIgnore},
			func(r *http.Request) { r.Header.Set("Cookie", "a=b") }, "HIT"},
		{"unlisted cookie", Options{TTL: time.Minute, LockTimeout: time.Second, Cookies: CookiesNamed, CookieNames: []string{"session"}},
			func(r *http.Request) { r.Header.Set("Cookie", "theme=dark") }, "HIT"},
		{"listed cookie", Options{TTL: time.Minute, LockTimeout: time.Second, Cookies: CookiesNamed, CookieNames: []string{"session"}},
			func(r *http.Request) { r.Header.Set("Cookie", "theme=dark; session=1") }, "BYPASS"},
		{"listed prefix", Options{TTL: time.Minute, LockTimeout: time.Second, Cookies: CookiesNamed, CookieNames: []string{"wordpress_logged_in_*"}},
			func(r *http.Request) { r.Header.Set("Cookie", "wordpress_logged_in_abc=1") }, "BYPASS"},
		{"prefix is not a substring", Options{TTL: time.Minute, LockTimeout: time.Second, Cookies: CookiesNamed, CookieNames: []string{"wp_*"}},
			func(r *http.Request) { r.Header.Set("Cookie", "awp_x=1") }, "HIT"},
		{"cookie name needs the whole name", Options{TTL: time.Minute, LockTimeout: time.Second, Cookies: CookiesNamed, CookieNames: []string{"id"}},
			func(r *http.Request) { r.Header.Set("Cookie", "idx=1") }, "HIT"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, 1<<20, 1<<16)
			h := page("data")
			// Warm the cache with a plain request, then repeat with the property.
			e.do("GET", "/b", c.o, nil, h)
			got := e.do("GET", "/b", c.o, c.mod, h)
			if xcache(got) != c.want {
				t.Fatalf("X-Cache = %q, want %q", xcache(got), c.want)
			}
			if c.want == "BYPASS" {
				if e.calls.Load() != 2 {
					t.Fatalf("a bypassing request must reach the backend (calls=%d)", e.calls.Load())
				}
				// And it stores nothing: a response for a logged-in client must not be shared.
				e2 := newEnv(t, 1<<20, 1<<16)
				e2.do("GET", "/b", c.o, c.mod, h)
				if e2.s.Stats().Entries != 0 {
					t.Fatal("a bypassing request stored its response")
				}
			}
		})
	}
}

func TestHeadAndRange(t *testing.T) {
	e := newEnv(t, 1<<20, 1<<16)
	h := page("0123456789", "ETag", `"v1"`, "Last-Modified", "Mon, 01 Jun 2026 10:00:00 GMT", "Content-Type", "text/plain")
	// A HEAD on a miss neither stores nor is served from a body.
	if got := e.do("HEAD", "/r", defaults(), nil, h); xcache(got) != "MISS" || e.s.Stats().Entries != 0 {
		t.Fatalf("HEAD miss: %s entries=%d", xcache(got), e.s.Stats().Entries)
	}
	// A Range request on a miss isn't stored either: the response is a part.
	partial := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 2-4/10")
		w.WriteHeader(206)
		io.WriteString(w, "234")
	}
	e.do("GET", "/r", defaults(), func(r *http.Request) { r.Header.Set("Range", "bytes=2-4") }, partial)
	if e.s.Stats().Entries != 0 {
		t.Fatal("a partial response was stored")
	}
	e.do("GET", "/r", defaults(), nil, h)
	if e.s.Stats().Entries != 1 {
		t.Fatal("whole response not stored")
	}

	head := e.do("HEAD", "/r", defaults(), nil, h)
	if xcache(head) != "HIT" || head.Body.Len() != 0 || head.Header().Get("Content-Length") != "10" {
		t.Fatalf("HEAD hit: %s len=%d cl=%s", xcache(head), head.Body.Len(), head.Header().Get("Content-Length"))
	}
	rng := e.do("GET", "/r", defaults(), func(r *http.Request) { r.Header.Set("Range", "bytes=2-4") }, h)
	if rng.Code != 206 || rng.Body.String() != "234" || xcache(rng) != "HIT" || rng.Header().Get("Content-Range") != "bytes 2-4/10" {
		t.Fatalf("range hit: %d %q %s %q", rng.Code, rng.Body.String(), xcache(rng), rng.Header().Get("Content-Range"))
	}
	nm := e.do("GET", "/r", defaults(), func(r *http.Request) { r.Header.Set("If-None-Match", `"v1"`) }, h)
	if nm.Code != 304 || nm.Body.Len() != 0 {
		t.Fatalf("If-None-Match: %d %q", nm.Code, nm.Body.String())
	}
	ims := e.do("GET", "/r", defaults(), func(r *http.Request) { r.Header.Set("If-Modified-Since", "Mon, 01 Jun 2026 11:00:00 GMT") }, h)
	if ims.Code != 304 {
		t.Fatalf("If-Modified-Since: %d", ims.Code)
	}
	other := e.do("GET", "/r", defaults(), func(r *http.Request) { r.Header.Set("If-None-Match", `"other"`) }, h)
	if other.Code != 200 || other.Body.String() != "0123456789" {
		t.Fatalf("different ETag: %d", other.Code)
	}
	if e.calls.Load() != 3 { // the HEAD miss, the partial response and the whole one; everything else was a hit
		t.Fatalf("backend ran %d times, want 3", e.calls.Load())
	}
}

func TestWritesInvalidate(t *testing.T) {
	e := newEnv(t, 1<<20, 1<<16)
	n := 0
	get := func(w http.ResponseWriter, r *http.Request) { n++; fmt.Fprint(w, n) }
	e.do("GET", "/items", defaults(), nil, get)
	e.do("GET", "/items?page=2", defaults(), nil, get)
	e.do("GET", "/other", defaults(), nil, get)
	if e.s.Stats().Entries != 3 {
		t.Fatal("setup")
	}
	// A failed write changes nothing.
	e.do("POST", "/items", defaults(), nil, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	if e.s.Stats().Entries != 3 {
		t.Fatal("a failed POST invalidated")
	}
	e.do("POST", "/items", defaults(), nil, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) })
	if got := e.s.Stats().Entries; got != 1 {
		t.Fatalf("entries after POST = %d, want 1 (other path stays)", got)
	}
	for _, m := range []string{"PUT", "DELETE", "PATCH"} {
		e.do("GET", "/items", defaults(), nil, get)
		e.do(m, "/items", defaults(), nil, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
		if xcache(e.do("GET", "/items", defaults(), nil, get)) != "MISS" {
			t.Fatalf("%s did not invalidate", m)
		}
	}
	// A write that sends no explicit status is a 200.
	e.do("POST", "/other", defaults(), nil, page("done"))
	if xcache(e.do("GET", "/other", defaults(), nil, get)) != "MISS" {
		t.Fatal("implicit 200 did not invalidate")
	}
	// Other methods are passed through untouched.
	before := e.s.Stats()
	rec := e.do("OPTIONS", "/items", defaults(), nil, page("opts"))
	if rec.Body.String() != "opts" || rec.Header().Get("X-Cache") != "" || e.s.Stats().Stored != before.Stored {
		t.Fatal("OPTIONS must pass through")
	}
}

func TestSizeLimits(t *testing.T) {
	e := newEnv(t, 1<<20, 100)
	big := strings.Repeat("x", 101)
	// A declared length over the limit: not even copied.
	e.do("GET", "/big", defaults(), nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(big)))
		io.WriteString(w, big)
	})
	// An undeclared length that turns out to be too large.
	e.do("GET", "/chunked", defaults(), nil, func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 11; i++ {
			io.WriteString(w, strings.Repeat("y", 10))
		}
	})
	if e.s.Stats().Entries != 0 {
		t.Fatal("an oversize response was stored")
	}
	if got := e.do("GET", "/big", defaults(), nil, page("small")); xcache(got) != "MISS" || got.Body.String() != "small" {
		t.Fatalf("an oversize response must not leave an entry behind: %s %q", xcache(got), got.Body.String())
	}
	// Exactly at the limit is fine.
	e.do("GET", "/edge", defaults(), nil, page(strings.Repeat("z", 100)))
	if xcache(e.do("GET", "/edge", defaults(), nil, page("other"))) != "HIT" {
		t.Fatal("a response of exactly max_entry_size was refused")
	}
	// The client still got every byte of the oversize responses.
	rec := e.do("GET", "/chunked2", defaults(), nil, func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 11; i++ {
			io.WriteString(w, strings.Repeat("y", 10))
		}
	})
	if rec.Body.Len() != 110 {
		t.Fatalf("client got %d bytes", rec.Body.Len())
	}
}

func TestIncompleteResponsesAreNotStored(t *testing.T) {
	e := newEnv(t, 1<<20, 1<<16)
	// Declares more than it sends.
	e.do("GET", "/short", defaults(), nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "50")
		io.WriteString(w, "only ten b")
	})
	// A backend that dies half way: the handler panics with ErrAbortHandler.
	func() {
		defer func() { _ = recover() }()
		e.do("GET", "/abort", defaults(), nil, func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "half")
			panic(http.ErrAbortHandler)
		})
	}()
	if e.s.Stats().Entries != 0 {
		t.Fatal("an incomplete response was stored")
	}
	// The flight of the aborted request is released.
	done := make(chan struct{})
	go func() { e.do("GET", "/abort", defaults(), nil, page("ok")); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("a request after an abort waited for a flight that never ended")
	}
}

type failingWriter struct {
	http.ResponseWriter
}

func (failingWriter) Write(p []byte) (int, error) { return 0, io.ErrClosedPipe }

func TestClientGoneIsNotStored(t *testing.T) {
	e := newEnv(t, 1<<20, 1<<16)
	req := httptest.NewRequest("GET", "/gone", nil)
	rec := httptest.NewRecorder()
	e.s.Serve(failingWriter{rec}, req, keyOf(req), defaults(), func(w http.ResponseWriter) { io.WriteString(w, "data") })
	if e.s.Stats().Entries != 0 {
		t.Fatal("a response the client did not receive was stored")
	}
}

func TestCoalescing(t *testing.T) {
	e := newEnv(t, 1<<20, 1<<16)
	var calls atomic.Int32
	release := make(chan struct{})
	h := func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-release
		w.Header().Set("Cache-Control", "max-age=60")
		io.WriteString(w, "shared")
	}
	const n = 20
	var wg sync.WaitGroup
	results := make([]*httptest.ResponseRecorder, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/herd", nil)
			rec := httptest.NewRecorder()
			e.s.Serve(rec, req, keyOf(req), defaults(), func(w http.ResponseWriter) { h(w, req) })
			results[i] = rec
		}(i)
	}
	// Let everybody queue up behind the first request.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		e.s.flMu.Lock()
		have := len(e.s.flights)
		e.s.flMu.Unlock()
		if have == 1 && calls.Load() == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("backend ran %d times for %d simultaneous requests", calls.Load(), n)
	}
	miss, hit := 0, 0
	for _, r := range results {
		if r.Body.String() != "shared" {
			t.Fatalf("body %q", r.Body.String())
		}
		switch xcache(r) {
		case "MISS":
			miss++
		case "HIT":
			hit++
		}
	}
	if miss != 1 || hit != n-1 {
		t.Fatalf("miss=%d hit=%d", miss, hit)
	}
	e.s.flMu.Lock()
	left := len(e.s.flights)
	e.s.flMu.Unlock()
	if left != 0 {
		t.Fatalf("%d flights left behind", left)
	}
}

func TestCoalescingDoesNotSerializeUncacheable(t *testing.T) {
	e := newEnv(t, 1<<20, 1<<16)
	slowLock := Options{TTL: time.Minute, LockTimeout: time.Minute} // waiting for the lock would show
	var calls, inside atomic.Int32
	var peak atomic.Int32
	gate := make(chan struct{})
	h := func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		cur := inside.Add(1)
		for {
			p := peak.Load()
			if cur <= p || peak.CompareAndSwap(p, cur) {
				break
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(200) // the decision is made here: nobody has to wait for the body
		<-gate
		io.WriteString(w, "private")
		inside.Add(-1)
	}
	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/nostore", nil)
			rec := httptest.NewRecorder()
			e.s.Serve(rec, req, keyOf(req), slowLock, func(w http.ResponseWriter) { h(w, req) })
		}()
	}
	deadline := time.Now().Add(3 * time.Second)
	for inside.Load() < n && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := inside.Load(); got != n {
		t.Fatalf("only %d of %d requests reached the backend at the same time: uncacheable responses are serialized", got, n)
	}
	close(gate)
	wg.Wait()
}

func TestLockTimeout(t *testing.T) {
	e := newEnv(t, 1<<20, 1<<16)
	o := Options{TTL: time.Minute, LockTimeout: 80 * time.Millisecond}
	release := make(chan struct{})
	leaderIn := make(chan struct{})
	var calls atomic.Int32
	slow := func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(leaderIn)
			<-release
		}
		io.WriteString(w, "x")
	}
	done := make(chan struct{})
	go func() {
		req := httptest.NewRequest("GET", "/slow", nil)
		e.s.Serve(httptest.NewRecorder(), req, keyOf(req), o, func(w http.ResponseWriter) { slow(w, req) })
		close(done)
	}()
	<-leaderIn
	start := time.Now()
	req := httptest.NewRequest("GET", "/slow", nil)
	rec := httptest.NewRecorder()
	e.s.Serve(rec, req, keyOf(req), o, func(w http.ResponseWriter) { slow(w, req) })
	waited := time.Since(start)
	if rec.Body.String() != "x" || xcache(rec) != "MISS" {
		t.Fatalf("follower after timeout: %q %s", rec.Body.String(), xcache(rec))
	}
	if waited < 60*time.Millisecond || waited > 2*time.Second {
		t.Fatalf("follower waited %v, want about the lock timeout", waited)
	}
	close(release)
	<-done
}

func TestLockOff(t *testing.T) {
	e := newEnv(t, 1<<20, 1<<16)
	o := Options{TTL: time.Minute} // LockTimeout 0
	release := make(chan struct{})
	var inside atomic.Int32
	h := func(w http.ResponseWriter, r *http.Request) {
		inside.Add(1)
		<-release
		io.WriteString(w, "x")
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/nolock", nil)
			e.s.Serve(httptest.NewRecorder(), req, keyOf(req), o, func(w http.ResponseWriter) { h(w, req) })
		}()
	}
	deadline := time.Now().Add(3 * time.Second)
	for inside.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if inside.Load() != 3 {
		t.Fatalf("with the lock off all requests must reach the backend, got %d", inside.Load())
	}
	close(release)
	wg.Wait()
}

func TestFollowerGoneWhileWaiting(t *testing.T) {
	e := newEnv(t, 1<<20, 1<<16)
	release := make(chan struct{})
	in := make(chan struct{})
	go func() {
		req := httptest.NewRequest("GET", "/w", nil)
		e.s.Serve(httptest.NewRecorder(), req, keyOf(req), defaults(), func(w http.ResponseWriter) {
			close(in)
			<-release
			io.WriteString(w, "x")
		})
	}()
	<-in
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/w", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		e.s.Serve(rec, req, keyOf(req), defaults(), func(w http.ResponseWriter) { t.Error("a gone client reached the backend") })
		close(done)
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a follower whose client left kept waiting")
	}
	if rec.Code != 499 {
		t.Fatalf("status %d, want 499", rec.Code)
	}
	close(release)
}

func TestStaleIfError(t *testing.T) {
	o := Options{TTL: 10 * time.Second, StaleIfError: time.Minute, LockTimeout: time.Second}
	warm := func(e *env, hdr ...string) {
		e.do("GET", "/s", o, nil, page("good", hdr...))
		e.advance(11 * time.Second) // now stale
	}
	fail := func(code int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			io.WriteString(w, "backend error")
		}
	}
	for _, code := range []int{500, 502, 503, 504} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			e := newEnv(t, 1<<20, 1<<16)
			warm(e)
			rec := e.do("GET", "/s", o, nil, fail(code))
			if rec.Code != 200 || rec.Body.String() != "good" || xcache(rec) != "STALE" {
				t.Fatalf("%d -> %d %q %s", code, rec.Code, rec.Body.String(), xcache(rec))
			}
			if e.s.Stats().Stale != 1 {
				t.Fatalf("stats: %+v", e.s.Stats())
			}
		})
	}
	t.Run("other errors are the backend's answer", func(t *testing.T) {
		e := newEnv(t, 1<<20, 1<<16)
		warm(e)
		for _, code := range []int{400, 404, 501} {
			if rec := e.do("GET", "/s", o, nil, fail(code)); rec.Code != code || xcache(rec) != "MISS" {
				t.Fatalf("%d: got %d %s", code, rec.Code, xcache(rec))
			}
		}
	})
	t.Run("a healthy backend replaces the entry", func(t *testing.T) {
		e := newEnv(t, 1<<20, 1<<16)
		warm(e)
		rec := e.do("GET", "/s", o, nil, page("new"))
		if rec.Body.String() != "new" || xcache(rec) != "MISS" {
			t.Fatalf("%q %s", rec.Body.String(), xcache(rec))
		}
		if got := e.do("GET", "/s", o, nil, fail(500)); xcache(got) != "HIT" || got.Body.String() != "new" {
			t.Fatalf("the new entry must be served fresh: %q %s", got.Body.String(), xcache(got))
		}
	})
	t.Run("not past the window", func(t *testing.T) {
		e := newEnv(t, 1<<20, 1<<16)
		warm(e)
		e.advance(time.Minute)
		if rec := e.do("GET", "/s", o, nil, fail(500)); rec.Code != 500 {
			t.Fatalf("an entry past the stale window was served: %d", rec.Code)
		}
	})
	t.Run("must-revalidate forbids it", func(t *testing.T) {
		e := newEnv(t, 1<<20, 1<<16)
		warm(e, "Cache-Control", "max-age=10, must-revalidate")
		if rec := e.do("GET", "/s", o, nil, fail(500)); rec.Code != 500 {
			t.Fatalf("stale served despite must-revalidate: %d", rec.Code)
		}
	})
	t.Run("off by default", func(t *testing.T) {
		e := newEnv(t, 1<<20, 1<<16)
		off := Options{TTL: 10 * time.Second, LockTimeout: time.Second}
		e.do("GET", "/s", off, nil, page("good"))
		e.advance(11 * time.Second)
		if rec := e.do("GET", "/s", off, nil, fail(500)); rec.Code != 500 {
			t.Fatalf("stale served with stale_if_error 0: %d", rec.Code)
		}
	})
	t.Run("transport failure", func(t *testing.T) {
		e := newEnv(t, 1<<20, 1<<16)
		warm(e)
		req := httptest.NewRequest("GET", "/s", nil)
		rec := httptest.NewRecorder()
		served := false
		e.s.Serve(rec, req, keyOf(req), o, func(w http.ResponseWriter) {
			ss, ok := w.(interface{ ServeStale() bool })
			if !ok {
				t.Fatal("the writer has no ServeStale")
			}
			served = ss.ServeStale()
		})
		if !served || rec.Body.String() != "good" || xcache(rec) != "STALE" {
			t.Fatalf("%v %q %s", served, rec.Body.String(), xcache(rec))
		}
	})
	t.Run("no entry no stale", func(t *testing.T) {
		e := newEnv(t, 1<<20, 1<<16)
		req := httptest.NewRequest("GET", "/none", nil)
		e.s.Serve(httptest.NewRecorder(), req, keyOf(req), o, func(w http.ResponseWriter) {
			if w.(interface{ ServeStale() bool }).ServeStale() {
				t.Fatal("stale from nothing")
			}
		})
	})
	t.Run("not after the response started", func(t *testing.T) {
		e := newEnv(t, 1<<20, 1<<16)
		warm(e)
		req := httptest.NewRequest("GET", "/s", nil)
		e.s.Serve(httptest.NewRecorder(), req, keyOf(req), o, func(w http.ResponseWriter) {
			w.WriteHeader(200)
			if w.(interface{ ServeStale() bool }).ServeStale() {
				t.Fatal("stale after the status was written")
			}
		})
	})
}

func TestFollowersGetStaleWhenTheLeaderFailed(t *testing.T) {
	o := Options{TTL: 10 * time.Second, StaleIfError: time.Minute, LockTimeout: time.Minute}
	e := newEnv(t, 1<<20, 1<<16)
	e.do("GET", "/f", o, nil, page("good"))
	e.advance(11 * time.Second)

	release, followersDone := make(chan struct{}), make(chan struct{})
	var backend, finished atomic.Int32
	h := func(w http.ResponseWriter, r *http.Request) {
		if backend.Add(1) == 1 {
			<-release
			w.WriteHeader(503)
			<-followersDone // the leader's handler is still running while the followers are served
			return
		}
		w.WriteHeader(503)
	}
	const n = 6
	var wg sync.WaitGroup
	recs := make([]*httptest.ResponseRecorder, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/f", nil)
			rec := httptest.NewRecorder()
			e.s.Serve(rec, req, keyOf(req), o, func(w http.ResponseWriter) { h(w, req) })
			recs[i] = rec
			finished.Add(1)
		}(i)
	}
	deadline := time.Now().Add(3 * time.Second)
	for backend.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(80 * time.Millisecond)
	close(release)
	for finished.Load() < n-1 && time.Now().Before(deadline.Add(3*time.Second)) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := finished.Load(); got != n-1 {
		t.Fatalf("%d of %d followers were served while the leader's handler ran; they must not wait for it", got, n-1)
	}
	close(followersDone)
	wg.Wait()
	if backend.Load() != 1 {
		t.Fatalf("backend ran %d times; followers should have used the stale entry", backend.Load())
	}
	for i, r := range recs {
		if r.Code != 200 || r.Body.String() != "good" || xcache(r) != "STALE" {
			t.Fatalf("request %d: %d %q %s", i, r.Code, r.Body.String(), xcache(r))
		}
	}
}

func TestFollowersDoNotGetStaleWhenTheLeaderSucceeded(t *testing.T) {
	// The leader got a good answer that can't be stored. Followers must not fall
	// back to the old entry: nothing failed.
	o := Options{TTL: 10 * time.Second, StaleIfError: time.Minute, LockTimeout: 3 * time.Second}
	e := newEnv(t, 1<<20, 1<<16)
	e.do("GET", "/g", o, nil, page("old"))
	e.advance(11 * time.Second)

	release := make(chan struct{})
	var backend atomic.Int32
	h := func(w http.ResponseWriter, r *http.Request) {
		if backend.Add(1) == 1 {
			<-release
		}
		w.Header().Set("Cache-Control", "no-store")
		io.WriteString(w, "new and private")
	}
	const n = 5
	var wg sync.WaitGroup
	recs := make([]*httptest.ResponseRecorder, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/g", nil)
			rec := httptest.NewRecorder()
			e.s.Serve(rec, req, keyOf(req), o, func(w http.ResponseWriter) { h(w, req) })
			recs[i] = rec
		}(i)
	}
	deadline := time.Now().Add(3 * time.Second)
	for backend.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(80 * time.Millisecond)
	close(release)
	wg.Wait()
	for i, r := range recs {
		if r.Body.String() != "new and private" {
			t.Fatalf("request %d got %q (%s)", i, r.Body.String(), xcache(r))
		}
	}
}

func TestEvictionIsLeastRecentlyUsed(t *testing.T) {
	body := strings.Repeat("b", 1000)
	probe := newEnv(t, 1<<20, 1<<16)
	probe.do("GET", "/k0", defaults(), nil, page(body))
	per := probe.s.Stats().Bytes // what one of these entries costs

	e := newEnv(t, 3*per+per/2, 1<<16) // room for three
	get := func(i int) *httptest.ResponseRecorder {
		return e.do("GET", fmt.Sprintf("/k%d", i), defaults(), nil, page(body))
	}
	get(0)
	get(1)
	get(2)
	if n := e.s.Stats().Entries; n != 3 {
		t.Fatalf("entries %d, want 3 (an entry costs %d, max %d)", n, per, e.s.Stats().Max)
	}
	get(0) // 0 is now the most recently used
	get(3) // evicts 1, the least recently used
	if xcache(get(0)) != "HIT" {
		t.Fatal("recently used entry was evicted")
	}
	if xcache(get(1)) != "MISS" {
		t.Fatal("least recently used entry survived")
	}
	st := e.s.Stats()
	if st.Evicted == 0 || st.Bytes > st.Max {
		t.Fatalf("stats: %+v", st)
	}
}

func TestEntryLargerThanTheCacheIsRefused(t *testing.T) {
	e := newEnv(t, 2000, 1<<20)
	e.do("GET", "/huge", defaults(), nil, page(strings.Repeat("h", 5000)))
	if e.s.Stats().Entries != 0 || e.s.Stats().Bytes != 0 {
		t.Fatalf("stats: %+v", e.s.Stats())
	}
}

func TestAccountingReturnsToZero(t *testing.T) {
	e := newEnv(t, 1<<20, 1<<16)
	for i := 0; i < 20; i++ {
		e.do("GET", fmt.Sprintf("/a%d", i%5), defaults(), nil, page(strings.Repeat("a", 100+i)))
		e.advance(2 * time.Minute) // everything expires between requests
	}
	e.s.Purge(Match{All: true})
	st := e.s.Stats()
	if st.Bytes != 0 || st.Entries != 0 || len(e.s.paths) != 0 || e.s.lru.Len() != 0 {
		t.Fatalf("leftovers: %+v paths=%d lru=%d", st, len(e.s.paths), e.s.lru.Len())
	}
	if e.s.capturing.Load() != 0 {
		t.Fatalf("capture budget leaked: %d", e.s.capturing.Load())
	}
}

func TestCaptureBudget(t *testing.T) {
	// The cache may hold 4000 bytes in total, so two responses of 3000 bytes that
	// are being copied at the same moment can't both be.
	e := newEnv(t, 4000, 3500)
	gate := make(chan struct{})
	end := make(chan struct{})
	var in, wrote sync.WaitGroup
	in.Add(2)
	wrote.Add(2)
	h := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		io.WriteString(w, strings.Repeat("c", 100))
		in.Done()
		<-gate
		io.WriteString(w, strings.Repeat("c", 2900))
		wrote.Done()
		<-end // both responses are being copied at this moment
	}
	var wg sync.WaitGroup
	for _, p := range []string{"/c1", "/c2"} {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			req := httptest.NewRequest("GET", p, nil)
			rec := httptest.NewRecorder()
			e.s.Serve(rec, req, keyOf(req), defaults(), func(w http.ResponseWriter) { h(w, req) })
			if rec.Body.Len() != 3000 {
				t.Errorf("%s: client got %d bytes", p, rec.Body.Len())
			}
		}(p)
	}
	in.Wait()
	close(gate)
	wrote.Wait()
	close(end)
	wg.Wait()
	if e.s.capturing.Load() != 0 {
		t.Fatalf("capture budget leaked: %d", e.s.capturing.Load())
	}
	st := e.s.Stats()
	if st.Bytes > st.Max {
		t.Fatalf("over the limit: %+v", st)
	}
	if st.Stored != 1 {
		t.Fatalf("%d responses stored; the copy budget allows only one of the two big responses at a time", st.Stored)
	}
}

func TestPurgeAndInvalidate(t *testing.T) {
	e := newEnv(t, 1<<20, 1<<16)
	fill := func(host, path string) {
		req := httptest.NewRequest("GET", path, nil)
		u, _ := url.Parse(path)
		k := Key{Host: host, Path: u.EscapedPath(), Query: u.RawQuery, Scheme: "https"}
		e.s.Serve(httptest.NewRecorder(), req, k, defaults(), func(w http.ResponseWriter) { io.WriteString(w, "x") })
	}
	setup := func() {
		e.s.Purge(Match{All: true})
		for _, h := range []string{"a.test", "b.test"} {
			for _, p := range []string{"/blog/one", "/blog/two?x=1", "/blog/two", "/shop/one"} {
				fill(h, p)
			}
		}
		if e.s.Stats().Entries != 8 {
			t.Fatalf("setup: %d entries", e.s.Stats().Entries)
		}
	}
	cases := []struct {
		name string
		m    Match
		want int
	}{
		{"all", Match{All: true}, 8},
		{"one path on every host", Match{Path: "/blog/two"}, 4}, // with and without query, two hosts
		{"one path on one host", Match{Host: "a.test", Path: "/blog/two"}, 2},
		{"prefix", Match{Prefix: "/blog/"}, 6},
		{"prefix on a host", Match{Host: "B.TEST", Prefix: "/blog/"}, 3},
		{"no such path", Match{Path: "/nope"}, 0},
		{"a prefix is not a path", Match{Path: "/blog"}, 0},
		{"host alone selects nothing", Match{Host: "a.test"}, 0},
		{"empty match selects nothing", Match{}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setup()
			if got := e.s.Purge(c.m); got != c.want {
				t.Fatalf("purged %d, want %d", got, c.want)
			}
			if left := e.s.Stats().Entries; left != 8-c.want {
				t.Fatalf("%d entries left, want %d", left, 8-c.want)
			}
		})
	}
	setup()
	if n := e.s.Invalidate(Key{Host: "a.test", Path: "/blog/two", Scheme: "http"}); n != 2 {
		t.Fatalf("Invalidate removed %d, want 2 (every query and scheme of the path)", n)
	}
}

func TestPresetHeadersAreNotOverridden(t *testing.T) {
	e := newEnv(t, 1<<20, 1<<16)
	run := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/hdr", nil)
		rec := httptest.NewRecorder()
		rec.Header().Set("X-Frame-Options", "DENY") // the server's security header
		rec.Header().Set("Server", "lotus")
		e.s.Serve(rec, req, keyOf(req), defaults(), func(w http.ResponseWriter) {
			w.Header().Set("X-Frame-Options", "ALLOWALL")
			w.Header().Set("Server", "apache")
			w.Header().Set("X-App", "1")
			io.WriteString(w, "x")
		})
		return rec
	}
	for i, label := range []string{"MISS", "HIT"} {
		rec := run()
		if got := rec.Header().Values("X-Frame-Options"); len(got) != 1 || got[0] != "DENY" {
			t.Fatalf("%s: X-Frame-Options = %v", label, got)
		}
		if got := rec.Header().Values("Server"); len(got) != 1 || got[0] != "lotus" {
			t.Fatalf("%s: Server = %v", label, got)
		}
		if rec.Header().Get("X-App") != "1" || xcache(rec) != label {
			t.Fatalf("round %d: %v", i, rec.Header())
		}
	}
}

func TestStoredHeadersAreCleaned(t *testing.T) {
	e := newEnv(t, 1<<20, 1<<16)
	h := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", "Mon, 01 Jun 2026 00:00:00 GMT")
		w.Header().Set("Connection", "close")
		w.Header().Set("Keep-Alive", "timeout=5")
		w.Header().Set("Age", "0")
		w.Header().Set("X-Cache", "HIT from upstream")
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "body")
	}
	first := e.do("GET", "/clean", defaults(), nil, h)
	if xcache(first) != "MISS" {
		t.Fatalf("a backend's own X-Cache must be replaced, got %q", first.Header().Values("X-Cache"))
	}
	second := e.do("GET", "/clean", defaults(), nil, h)
	for _, name := range []string{"Date", "Connection", "Keep-Alive"} {
		if v := second.Header().Get(name); v != "" {
			t.Fatalf("hit carries stored %s: %q", name, v)
		}
	}
	if got := second.Header().Values("X-Cache"); len(got) != 1 || got[0] != "HIT" {
		t.Fatalf("X-Cache = %v", got)
	}
}

func TestTrailersArePassedOn(t *testing.T) {
	e := newEnv(t, 1<<20, 1<<16)
	h := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Trailer", "X-Sum")
		io.WriteString(w, "body")
		w.Header().Set("X-Sum", "abc")
	}
	rec := e.do("GET", "/tr", defaults(), nil, h)
	if got := rec.Result().Trailer.Get("X-Sum"); got != "abc" {
		t.Fatalf("trailer = %q", got)
	}
	if e.s.Stats().Entries != 0 {
		t.Fatal("a response with trailers was stored")
	}
}

func TestFlushReachesTheClient(t *testing.T) {
	e := newEnv(t, 1<<20, 1<<16)
	h := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(200)
		io.WriteString(w, "a")
		w.(http.Flusher).Flush()
		io.WriteString(w, "b")
	}
	rec := e.do("GET", "/fl", defaults(), nil, h)
	if rec.Body.String() != "ab" || rec.Code != 200 || !rec.Flushed {
		t.Fatalf("%d %q flushed=%v", rec.Code, rec.Body.String(), rec.Flushed)
	}
	// A flush doesn't make the response a stream: proxies flush responses of
	// unknown length after every write, and those are still cacheable.
	if got := e.do("GET", "/fl", defaults(), nil, h); got.Body.String() != "ab" || xcache(got) != "HIT" {
		t.Fatalf("flushed response not cached: %q %s", got.Body.String(), xcache(got))
	}
}

func TestEarlyHintsPassThrough(t *testing.T) {
	e := newEnv(t, 1<<20, 1<<16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.s.Serve(w, r, Key{Host: "h", Path: "/eh", Scheme: "http"}, defaults(), func(cw http.ResponseWriter) {
			cw.Header().Set("Link", "</s.css>; rel=preload")
			cw.WriteHeader(http.StatusEarlyHints)
			cw.Header().Del("Link")
			cw.Header().Set("Content-Type", "text/plain")
			cw.WriteHeader(200)
			io.WriteString(cw, "final")
		})
	}))
	defer srv.Close()
	var hintLink string
	var hinted int
	trace := &httptrace.ClientTrace{Got1xxResponse: func(code int, h textproto.MIMEHeader) error {
		hinted = code
		hintLink = h.Get("Link")
		return nil
	}}
	req, _ := http.NewRequest("GET", srv.URL+"/eh", nil)
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if hinted != 103 || hintLink != "</s.css>; rel=preload" {
		t.Fatalf("early hints lost: %d %q", hinted, hintLink)
	}
	if resp.StatusCode != 200 || string(body) != "final" || resp.Header.Get("Link") != "" {
		t.Fatalf("final response: %d %q link=%q", resp.StatusCode, body, resp.Header.Get("Link"))
	}
	if resp.Header.Get("X-Cache") != "MISS" {
		t.Fatalf("X-Cache = %q", resp.Header.Get("X-Cache"))
	}
}

func TestStoredErrorPagesKeepTheirBody(t *testing.T) {
	e := newEnv(t, 1<<20, 1<<16)
	h := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=30")
		w.WriteHeader(404)
		io.WriteString(w, "nothing here")
	}
	e.do("GET", "/missing", defaults(), nil, h)
	got := e.do("GET", "/missing", defaults(), nil, h)
	if got.Code != 404 || got.Body.String() != "nothing here" || xcache(got) != "HIT" || got.Header().Get("Content-Length") != "12" {
		t.Fatalf("%d %q %s cl=%s", got.Code, got.Body.String(), xcache(got), got.Header().Get("Content-Length"))
	}
	if head := e.do("HEAD", "/missing", defaults(), nil, h); head.Code != 404 || head.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %q", head.Code, head.Body.String())
	}
}

func TestRangeRequestsOnAMissStoreNothingEvenWhenIgnored(t *testing.T) {
	// A backend that ignores Range sends the whole body with 200. It is not
	// stored from a Range request all the same: the rule is simple and the cost is
	// one more fetch.
	e := newEnv(t, 1<<20, 1<<16)
	e.do("GET", "/ig", defaults(), func(r *http.Request) { r.Header.Set("Range", "bytes=0-3") }, page("0123456789"))
	if e.s.Stats().Entries != 0 {
		t.Fatal("a response to a Range request was stored")
	}
}

func TestAddRefusesWhatCanNeverFit(t *testing.T) {
	s := New(6000, 1<<20)
	now := time.Now()
	mk := func(name string, body int) *Entry {
		return &Entry{pk: name, variant: "v", body: make([]byte, body), stored: now, fresh: now.Add(time.Hour), until: now.Add(time.Hour)}
	}
	for i := 0; i < 3; i++ {
		if !s.add(mk(fmt.Sprint("small", i), 1)) {
			t.Fatal("a small entry was refused")
		}
	}
	// The body fits, with the overhead of an entry it doesn't.
	for _, big := range []*Entry{mk("big", 7000), mk("edge", 6000-entryOverhead+1)} {
		if s.add(big) {
			t.Fatalf("%s: an entry larger than the cache was accepted", big.pk)
		}
	}
	if st := s.Stats(); st.Entries != 3 || st.Evicted != 0 {
		t.Fatalf("an entry that can never fit evicted others: %+v", st)
	}
	if !s.add(mk("fits", 5000)) {
		t.Fatal("an entry that fits after evicting was refused")
	}
}

func TestAnOversizeEntryDoesNotFlushTheCache(t *testing.T) {
	e := newEnv(t, 6000, 1<<20)
	for i := 0; i < 3; i++ {
		e.do("GET", fmt.Sprintf("/small%d", i), defaults(), nil, page("s"))
	}
	if e.s.Stats().Entries != 3 {
		t.Fatal("setup")
	}
	e.do("GET", "/huge", defaults(), nil, page(strings.Repeat("h", 7000)))
	if st := e.s.Stats(); st.Entries != 3 || st.Evicted != 0 {
		t.Fatalf("an entry that can never fit evicted others: %+v", st)
	}
}

func TestPurgePrefixMeansPrefix(t *testing.T) {
	e := newEnv(t, 1<<20, 1<<16)
	for _, p := range []string{"/blog/a", "/x/blog/b", "/blogger/c", "/blog"} {
		e.do("GET", p, defaults(), nil, page("x"))
	}
	if n := e.s.Purge(Match{Prefix: "/blog/"}); n != 1 {
		t.Fatalf("purged %d, want only /blog/a", n)
	}
	if xcache(e.do("GET", "/x/blog/b", defaults(), nil, page("x"))) != "HIT" || xcache(e.do("GET", "/blogger/c", defaults(), nil, page("x"))) != "HIT" {
		t.Fatal("a path that merely contains the prefix was purged")
	}
	if n := e.s.Purge(Match{Path: "/blog"}); n != 1 {
		t.Fatalf("purged %d, want exactly /blog", n)
	}
}

func TestEmptyBodyAnd204(t *testing.T) {
	e := newEnv(t, 1<<20, 1<<16)
	h := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }
	e.do("GET", "/nc", defaults(), nil, h)
	got := e.do("GET", "/nc", defaults(), nil, h)
	if got.Code != 204 || xcache(got) != "HIT" || got.Body.Len() != 0 {
		t.Fatalf("204 hit: %d %s %q", got.Code, xcache(got), got.Body.String())
	}
	redirect := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/to")
		w.WriteHeader(301)
	}
	e.do("GET", "/old", defaults(), nil, redirect)
	got = e.do("GET", "/old", defaults(), nil, redirect)
	if got.Code != 301 || got.Header().Get("Location") != "/to" || xcache(got) != "HIT" {
		t.Fatalf("301 hit: %d %v", got.Code, got.Header())
	}
	e.do("GET", "/empty", defaults(), nil, page(""))
	if xcache(e.do("GET", "/empty", defaults(), nil, page(""))) != "HIT" {
		t.Fatal("an empty 200 is a response like any other")
	}
}

func TestNothingWrittenStoresNothing(t *testing.T) {
	e := newEnv(t, 1<<20, 1<<16)
	e.do("GET", "/nothing", defaults(), nil, func(w http.ResponseWriter, r *http.Request) {})
	if e.s.Stats().Entries != 0 {
		t.Fatal("stored a response that was never written")
	}
	// The flight is released all the same.
	done := make(chan struct{})
	go func() { e.do("GET", "/nothing", defaults(), nil, page("x")); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("flight not released")
	}
}

func TestConfigureShrinksAtOnce(t *testing.T) {
	e := newEnv(t, 1<<20, 1<<16)
	for i := 0; i < 10; i++ {
		e.do("GET", fmt.Sprintf("/c%d", i), defaults(), nil, page(strings.Repeat("c", 1000)))
	}
	e.s.Configure(3000, 1<<16)
	st := e.s.Stats()
	if st.Bytes > 3000 || st.Entries == 0 || st.Entries >= 10 {
		t.Fatalf("after shrinking: %+v", st)
	}
	if st.Evicted == 0 {
		t.Fatal("evictions not counted")
	}
}
