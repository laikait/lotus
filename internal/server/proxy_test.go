package server

import (
	"bufio"
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
)

// upstreamOf builds a configured upstream over the given backends.
func upstreamOf(name string, addrs ...string) model.Upstream {
	u := model.Upstream{
		Name: name, Balance: "round_robin", TLSVerify: true,
		ConnectTimeout:  model.Duration(2 * time.Second),
		ResponseTimeout: model.Duration(3 * time.Second),
		IdleTimeout:     model.Duration(10 * time.Second),
		Retries:         1, MaxFails: 3, FailTimeout: model.Duration(5 * time.Second),
	}
	for _, a := range addrs {
		u.Backends = append(u.Backends, model.UpstreamBackend{Address: a, Weight: 1})
	}
	return u
}

// proxyServer starts the site with routes and upstreams.
func proxyServer(t *testing.T, ups []model.Upstream, routes []model.Route, mutate func(*model.Site)) (*harness, string) {
	t.Helper()
	cfg := routedConfig(t, mutate, routes)
	cfg.Upstreams = ups
	h := start(t, cfg)
	return h, "http://" + h.addr("http")
}

func proxyRoute(path string, a model.Actions) model.Route {
	return model.Route{Kind: model.RoutePrefix, Path: path, Actions: a}
}

// echoBackend answers with what it received.
func echoBackend(t *testing.T) (*httptest.Server, *sync.Map) {
	t.Helper()
	seen := &sync.Map{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen.Store("host", r.Host)
		seen.Store("uri", r.RequestURI)
		seen.Store("header", r.Header.Clone())
		seen.Store("body", string(body))
		seen.Store("method", r.Method)
		w.Header().Set("X-Backend", "yes")
		w.Header().Set("Server", "Apache/2.4 (leaky)")
		w.Header().Set("X-Frame-Options", "ALLOWALL")
		w.Header().Set("Set-Cookie", "a=1")
		w.Header().Add("Set-Cookie", "b=2")
		fmt.Fprintf(w, "echo %s %s", r.Method, r.URL.String())
	}))
	t.Cleanup(s.Close)
	return s, seen
}

func hdr(seen *sync.Map) http.Header {
	v, _ := seen.Load("header")
	h, _ := v.(http.Header)
	return h
}

func TestProxyForwardsRequestAndResponse(t *testing.T) {
	be, seen := echoBackend(t)
	_, base := proxyServer(t, []model.Upstream{upstreamOf("app", be.URL)},
		[]model.Route{proxyRoute("/app/", model.Actions{Proxy: "app"})},
		func(s *model.Site) { s.SecurityHeaders = []model.Header{{Name: "X-Frame-Options", Value: "DENY"}} })

	resp, body := do(t, "POST", base+"/app/a%20b/c?x=1&y=%2F", map[string]string{
		"X-Forwarded-For": "6.6.6.6", "X-Forwarded-Host": "evil.example", "X-Forwarded-Proto": "https",
		"Forwarded": "for=6.6.6.6", "X-Real-IP": "6.6.6.6", "Connection": "X-Secret", "X-Secret": "hop",
	}, "payload")
	if resp.StatusCode != 200 || !strings.HasPrefix(body, "echo POST /app/a%20b/c?x=1&y=%2F") {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
	if v, _ := seen.Load("body"); v != "payload" {
		t.Errorf("body: %v", v)
	}
	if v, _ := seen.Load("host"); v != "a.test" {
		t.Errorf("the client's Host is kept: %v", v)
	}
	h := hdr(seen)
	if h.Get("X-Forwarded-For") != "127.0.0.1" || h.Get("X-Forwarded-Host") != "a.test" || h.Get("X-Forwarded-Proto") != "http" {
		t.Errorf("forwarding headers must be LOTUS's own, not the client's: %v", h)
	}
	if h.Get("Forwarded") != "" || h.Get("X-Real-Ip") != "" {
		t.Errorf("forged headers reached the backend: %v", h)
	}
	if h.Get("X-Secret") != "" {
		t.Errorf("a header named in Connection is hop-by-hop: %v", h)
	}
	if h.Get("X-Request-Id") == "" {
		t.Error("no request id")
	}
	if resp.Header.Get("X-Backend") != "yes" {
		t.Errorf("response headers pass through: %v", resp.Header)
	}
	if got := resp.Header.Values("Server"); len(got) != 1 || got[0] != serverHeader {
		t.Errorf("Server = %v: the backend's identity must not leak", got)
	}
	if got := resp.Header.Values("X-Frame-Options"); len(got) != 1 || got[0] != "DENY" {
		t.Errorf("X-Frame-Options = %v: a backend can't override the site's security headers", got)
	}
	if got := resp.Header.Values("Set-Cookie"); len(got) != 2 {
		t.Errorf("both cookies must arrive: %v", got)
	}
}

func TestProxyUsesTheRewrittenPathAndRouteHeaders(t *testing.T) {
	be, seen := echoBackend(t)
	rt := proxyRoute("/api/", model.Actions{Proxy: "app", ProxyHostUpstream: true,
		ProxyHeaders: []model.Header{{Name: "X-Env", Value: "prod"}, {Name: "X-Drop", Value: ""}}})
	rt.Rewrites = []model.Rewrite{{Pattern: "^/api/(.*)$", Regexp: mustRe("^/api/(.*)$"), Target: "/v2/{re.1}", Flag: model.RewriteLast}}
	_, base := proxyServer(t, []model.Upstream{upstreamOf("app", be.URL)},
		[]model.Route{rt, proxyRoute("/v2/", model.Actions{Proxy: "app", ProxyHostUpstream: true})}, nil)
	_, body := do(t, "GET", base+"/api/users?id=7", map[string]string{"X-Drop": "x"}, "")
	if body != "echo GET /v2/users?id=7" {
		t.Errorf("%q", body)
	}
	if v, _ := seen.Load("host"); v != strings.TrimPrefix(be.URL, "http://") {
		t.Errorf("proxy_host upstream sends the backend's own address: %v", v)
	}
	// The rewritten request matched the /v2/ route, which has no extra headers.
	if hdr(seen).Get("X-Env") != "" {
		t.Errorf("headers of the first route applied to the second: %v", hdr(seen))
	}
	// A route without a rewrite keeps its own headers.
	_, base2 := proxyServer(t, []model.Upstream{upstreamOf("app", be.URL)},
		[]model.Route{proxyRoute("/", model.Actions{Proxy: "app", ProxyHeaders: []model.Header{{Name: "X-Env", Value: "prod"}, {Name: "X-Drop", Value: ""}}})}, nil)
	do(t, "GET", base2+"/x", map[string]string{"X-Drop": "x"}, "")
	if h := hdr(seen); h.Get("X-Env") != "prod" || h.Get("X-Drop") != "" {
		t.Errorf("proxy_set_header: %v", h)
	}
}

func TestProxyBackendResponsesAreNotReplacedByErrorPages(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "backend says no", 404)
	}))
	defer be.Close()
	_, base := proxyServer(t, []model.Upstream{upstreamOf("app", be.URL)},
		[]model.Route{proxyRoute("/", model.Actions{Proxy: "app"})},
		func(s *model.Site) {
			s.ErrorPages = map[int]string{404: "/404.html", 502: "/404.html", 503: "/404.html"}
		})
	resp, body := do(t, "GET", base+"/x", nil, "")
	if resp.StatusCode != 404 || !strings.Contains(body, "backend says no") {
		t.Errorf("the backend's own 404 must reach the client: %d %q", resp.StatusCode, body)
	}
}

func TestProxyFailuresAreGatewayErrors(t *testing.T) {
	dead := func() string {
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		a := l.Addr().String()
		l.Close()
		return "http://" + a
	}
	pages := func(s *model.Site) {
		s.ErrorPages = map[int]string{502: "/404.html", 503: "/404.html", 504: "/404.html"}
	}

	// Nothing listens: 502, shown with the site's error page.
	u := upstreamOf("app", dead())
	u.Retries, u.MaxFails = 0, 0
	_, base := proxyServer(t, []model.Upstream{u}, []model.Route{proxyRoute("/app/", model.Actions{Proxy: "app"})}, pages)
	if resp, body := do(t, "GET", base+"/app/x", nil, ""); resp.StatusCode != 502 || !strings.Contains(body, "custom-404") {
		t.Errorf("refused connection: %d %q", resp.StatusCode, body)
	}

	// A backend that is out of rotation: 503 with Retry-After.
	u = upstreamOf("app", dead())
	u.Retries, u.MaxFails, u.FailTimeout = 0, 1, model.Duration(time.Minute)
	_, base = proxyServer(t, []model.Upstream{u}, []model.Route{proxyRoute("/", model.Actions{Proxy: "app"})}, nil)
	do(t, "GET", base+"/", nil, "")
	if resp, _ := do(t, "GET", base+"/", nil, ""); resp.StatusCode != 503 || resp.Header.Get("Retry-After") == "" {
		t.Errorf("no backend: %d %v", resp.StatusCode, resp.Header)
	}

	// A backend that never answers: 504.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(600 * time.Millisecond) }))
	defer slow.Close()
	u = upstreamOf("app", slow.URL)
	u.ResponseTimeout = model.Duration(150 * time.Millisecond)
	_, base = proxyServer(t, []model.Upstream{u}, []model.Route{proxyRoute("/app/", model.Actions{Proxy: "app"})}, pages)
	if resp, _ := do(t, "GET", base+"/app/", nil, ""); resp.StatusCode != 504 {
		t.Errorf("timeout: %d", resp.StatusCode)
	}
}

func TestProxyRequestBodyLimit(t *testing.T) {
	be, _ := echoBackend(t)
	rt := proxyRoute("/", model.Actions{Proxy: "app"})
	_, base := proxyServer(t, []model.Upstream{upstreamOf("app", be.URL)}, []model.Route{rt},
		func(s *model.Site) { s.Limits.MaxRequestBody = 10 })
	if resp, _ := do(t, "POST", base+"/", nil, strings.Repeat("x", 100)); resp.StatusCode != 413 {
		t.Errorf("a declared oversize body: %d", resp.StatusCode)
	}
	// A body that is bigger than it said it would be, sent in chunks.
	req, _ := http.NewRequest("POST", base+"/", io.NopCloser(strings.NewReader(strings.Repeat("y", 100))))
	req.Host = "a.test"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 413 {
		t.Errorf("a chunked oversize body: %d", resp.StatusCode)
	}
}

func TestProxyRetriesAnotherBackend(t *testing.T) {
	good, _ := echoBackend(t)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := "http://" + l.Addr().String()
	l.Close()
	_, base := proxyServer(t, []model.Upstream{upstreamOf("app", dead, good.URL)},
		[]model.Route{proxyRoute("/", model.Actions{Proxy: "app"})}, nil)
	for i := 0; i < 6; i++ {
		if resp, body := do(t, "POST", base+"/p", nil, "data"); resp.StatusCode != 200 || !strings.HasPrefix(body, "echo POST") {
			t.Fatalf("request %d: %d %q", i, resp.StatusCode, body)
		}
	}
}

func TestProxyStreamsResponsesWithoutBuffering(t *testing.T) {
	release := make(chan struct{})
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		<-release
		fmt.Fprint(w, "data: two\n\n")
	}))
	defer be.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	_, base := proxyServer(t, []model.Upstream{upstreamOf("app", be.URL)}, []model.Route{proxyRoute("/", model.Actions{Proxy: "app"})}, nil)
	req, _ := http.NewRequest("GET", base+"/events", nil)
	req.Host = "a.test"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "data: one\n" {
		t.Fatalf("the first event must arrive while the backend is still going: %q %v", line, err)
	}
	close(release)
}

func TestProxyWebSocket(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "no upgrade", 400)
			return
		}
		c, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		rw.Flush()
		buf := make([]byte, 64)
		n, _ := rw.Read(buf)
		rw.WriteString("echo:" + string(buf[:n]))
		rw.Flush()
	}))
	defer be.Close()
	h, _ := proxyServer(t, []model.Upstream{upstreamOf("app", be.URL)}, []model.Route{proxyRoute("/ws", model.Actions{Proxy: "app"})}, nil)
	c, err := net.Dial("tcp", h.addr("http"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprint(c, "GET /ws HTTP/1.1\r\nHost: a.test\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: x3JJHMbDL1EzLkh9GBhXDw==\r\n\r\n")
	br := bufio.NewReader(c)
	status, _ := br.ReadString('\n')
	if !strings.Contains(status, "101") {
		t.Fatalf("status line %q", status)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	fmt.Fprint(c, "hello")
	got := make([]byte, 16)
	n, _ := io.ReadAtLeast(br, got, len("echo:hello"))
	if string(got[:n]) != "echo:hello" {
		t.Errorf("through the upgraded connection: %q", got[:n])
	}
}

func TestProxyIgnoresHtaccessAndFilesOfTheSite(t *testing.T) {
	be, _ := echoBackend(t)
	_, base := proxyServer(t, []model.Upstream{upstreamOf("app", be.URL)},
		[]model.Route{proxyRoute("/", model.Actions{Proxy: "app"})}, func(s *model.Site) {
			s.HTAccess = htConfig()
			writeFiles(t, s.Root, map[string]string{".htaccess": "Require all denied\n"})
		})
	// hello.txt exists in the site root, but the route proxies.
	if resp, body := do(t, "GET", base+"/hello.txt", nil, ""); resp.StatusCode != 200 || !strings.HasPrefix(body, "echo GET /hello.txt") {
		t.Errorf("%d %q", resp.StatusCode, body)
	}
}

func TestProxyBalancesAndHealthChecks(t *testing.T) {
	mk := func(name string) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" {
				if name == "sick" {
					http.Error(w, "x", 500)
				}
				return
			}
			io.WriteString(w, name)
		}))
		t.Cleanup(s.Close)
		return s
	}
	a, sick := mk("a"), mk("sick")
	u := upstreamOf("app", a.URL, sick.URL)
	u.Health = &model.HealthCheck{Path: "/health", Interval: model.Duration(50 * time.Millisecond), Timeout: model.Duration(time.Second), HealthyThreshold: 1, UnhealthyThreshold: 1}
	_, base := proxyServer(t, []model.Upstream{u}, []model.Route{proxyRoute("/", model.Actions{Proxy: "app"})}, nil)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		onlyA := true
		for i := 0; i < 6; i++ {
			if _, body := do(t, "GET", base+"/", nil, ""); body != "a" {
				onlyA = false
			}
		}
		if onlyA {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("a backend that fails its health check kept getting requests")
}

func TestProxyReloadReplacesUpstreams(t *testing.T) {
	a, _ := echoBackend(t)
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "second") }))
	defer b.Close()
	cfg := routedConfig(t, nil, []model.Route{proxyRoute("/", model.Actions{Proxy: "app"})})
	cfg.Upstreams = []model.Upstream{upstreamOf("app", a.URL)}
	h := start(t, cfg)
	base := "http://" + h.addr("http")
	if _, body := do(t, "GET", base+"/", nil, ""); !strings.HasPrefix(body, "echo") {
		t.Fatalf("%q", body)
	}
	cfg2 := routedConfig(t, nil, []model.Route{proxyRoute("/", model.Actions{Proxy: "app"})})
	cfg2.Upstreams = []model.Upstream{upstreamOf("app", b.URL)}
	cfg2.Sites[0].Root = cfg.Sites[0].Root
	cfg2.Listeners = cfg.Listeners
	if err := h.srv.Reload(cfg2); err != nil {
		t.Fatal(err)
	}
	if _, body := do(t, "GET", base+"/", nil, ""); body != "second" {
		t.Errorf("after reload: %q", body)
	}
}
