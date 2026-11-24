package server

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/config/model"
)

func rl(requests int, per time.Duration, burst int, key string) *model.RateLimit {
	return &model.RateLimit{Requests: requests, Per: model.Duration(per), Burst: burst, Key: key}
}

var loopback = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}

// fromClient makes a request that a trusted proxy (the test client) forwards for ip.
func fromClient(ip string) map[string]string { return map[string]string{"X-Forwarded-For": ip} }

func TestRateLimitPerClientAddress(t *testing.T) {
	var hits atomic.Int32
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); io.WriteString(w, "ok") }))
	defer be.Close()
	cfg := routedConfig(t, func(s *model.Site) { s.RateLimit = rl(3, time.Hour, 3, model.RateKeyIP) },
		[]model.Route{proxyRoute("/app/", model.Actions{Proxy: "app"}), {Kind: model.RoutePrefix, Path: "/"}})
	cfg.Upstreams = []model.Upstream{upstreamOf("app", be.URL)}
	cfg.TrustedProxies = loopback
	h := start(t, cfg)
	base := "http://" + h.addr("http")

	for i := 0; i < 3; i++ {
		if resp, _ := do(t, "GET", base+"/app/x", fromClient("203.0.113.5"), ""); resp.StatusCode != 200 {
			t.Fatalf("request %d: %d", i, resp.StatusCode)
		}
	}
	resp, body := do(t, "GET", base+"/app/x", fromClient("203.0.113.5"), "")
	if resp.StatusCode != 429 {
		t.Fatalf("4th request: %d %q", resp.StatusCode, body)
	}
	if n, err := strconv.Atoi(resp.Header.Get("Retry-After")); err != nil || n < 1 {
		t.Errorf("Retry-After = %q", resp.Header.Get("Retry-After"))
	}
	if hits.Load() != 3 {
		t.Errorf("the backend saw %d requests: a refused request must not reach it", hits.Load())
	}
	// Another client has its own allowance, and the limit covers the whole site.
	if resp, _ := do(t, "GET", base+"/app/x", fromClient("203.0.113.6"), ""); resp.StatusCode != 200 {
		t.Errorf("another client: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", base+"/hello.txt", fromClient("203.0.113.5"), ""); resp.StatusCode != 429 {
		t.Errorf("a static file for the same client: %d", resp.StatusCode)
	}
	// An IPv6 client is counted per /64.
	for i := 0; i < 3; i++ {
		do(t, "GET", base+"/hello.txt", fromClient(fmt.Sprintf("2001:db8:1:2::%x", i+1)), "")
	}
	if resp, _ := do(t, "GET", base+"/hello.txt", fromClient("2001:db8:1:2:ffff::9"), ""); resp.StatusCode != 429 {
		t.Errorf("a fourth address of the same /64: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", base+"/hello.txt", fromClient("2001:db8:1:3::1"), ""); resp.StatusCode != 200 {
		t.Errorf("another /64: %d", resp.StatusCode)
	}
}

func TestRateLimitGlobalAndRefill(t *testing.T) {
	cfg := routedConfig(t, func(s *model.Site) { s.RateLimit = rl(2, 300*time.Millisecond, 2, model.RateKeyGlobal) }, nil)
	cfg.TrustedProxies = loopback
	h := start(t, cfg)
	base := "http://" + h.addr("http")
	codes := func(ips ...string) (out []int) {
		for _, ip := range ips {
			resp, _ := do(t, "GET", base+"/hello.txt", fromClient(ip), "")
			out = append(out, resp.StatusCode)
		}
		return
	}
	if got := codes("1.1.1.1", "2.2.2.2", "3.3.3.3"); got[0] != 200 || got[1] != 200 || got[2] != 429 {
		t.Errorf("a global limit is shared by every client: %v", got)
	}
	time.Sleep(350 * time.Millisecond)
	if got := codes("4.4.4.4"); got[0] != 200 {
		t.Errorf("the allowance refills: %v", got)
	}
}

func TestRouteRateLimitsAddToTheSitesAndCountOnce(t *testing.T) {
	routes := []model.Route{
		{Kind: model.RoutePrefix, Path: "/slow/", RateLimit: rl(2, time.Hour, 2, model.RateKeyIP), Actions: model.Actions{TryFiles: []string{"/page.html"}}},
		{Kind: model.RoutePrefix, Path: "/", Actions: model.Actions{TryFiles: []string{"{uri}", "=404"}}},
	}
	// A rewrite sends /redir/ through the limited route as an internal redirect.
	routes = append(routes, model.Route{Kind: model.RoutePrefix, Path: "/redir/", Rewrites: []model.Rewrite{
		{Pattern: "^/redir/(.*)$", Regexp: mustRe("^/redir/(.*)$"), Target: "/slow/{re.1}", Flag: model.RewriteLast}}})
	cfg := routedConfig(t, func(s *model.Site) { s.RateLimit = rl(100, time.Hour, 100, model.RateKeyIP) }, routes)
	cfg.TrustedProxies = loopback
	h := start(t, cfg)
	base := "http://" + h.addr("http")
	get := func(path, ip string) int {
		resp, _ := do(t, "GET", base+path, fromClient(ip), "")
		return resp.StatusCode
	}

	seq := func(path, ip string) [3]int { return [3]int{get(path, ip), get(path, ip), get(path, ip)} }
	if got := seq("/slow/a", "9.9.9.9"); got != [3]int{200, 200, 429} {
		t.Errorf("the route's own limit: %v", got)
	}
	if get("/hello.txt", "9.9.9.9") != 200 {
		t.Error("the limit of one route must not affect the others")
	}
	// Through the redirect each request is charged once to the route, not twice.
	if got := seq("/redir/a", "8.8.8.8"); got != [3]int{200, 200, 429} {
		t.Errorf("an internal redirect was counted more than once: %v", got)
	}
}

func TestRateLimitedRequestsDoNotCheckPasswords(t *testing.T) {
	cfg := routedConfig(t, func(s *model.Site) {
		s.HTAccess = htConfig()
		s.RateLimit = rl(2, time.Hour, 2, model.RateKeyIP)
		writeFiles(t, s.Root, map[string]string{
			".htaccess": "AuthType Basic\nAuthName x\nAuthUserFile .htpasswd\nRequire valid-user\n", ".htpasswd": htpasswdFile,
		})
	}, nil)
	h := start(t, cfg)
	base := "http://" + h.addr("http")
	do(t, "GET", base+"/hello.txt", nil, "")
	do(t, "GET", base+"/hello.txt", nil, "")
	if resp, _ := do(t, "GET", base+"/hello.txt", basic("alice", "secret"), ""); resp.StatusCode != 429 {
		t.Errorf("a refused request is not authenticated: %d", resp.StatusCode)
	}
}

// ---- connections per address ----

func dialN(t *testing.T, addr string, n int) []net.Conn {
	t.Helper()
	var conns []net.Conn
	for i := 0; i < n; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		t.Cleanup(func() { c.Close() })
	}
	return conns
}

// closedByServer says whether the server closes c without a request.
func closedByServer(c net.Conn) bool {
	c.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	_, err := c.Read(make([]byte, 1))
	ne, isNet := err.(net.Error)
	return err != nil && !(isNet && ne.Timeout())
}

func TestConnectionsPerAddress(t *testing.T) {
	cfg := routedConfig(t, nil, nil)
	cfg.Limits.MaxConnectionsPerIP = 2
	cfg.Sites[0].Limits.MaxConnectionsPerIP = 2
	h := start(t, cfg)
	conns := dialN(t, h.addr("http"), 3)
	time.Sleep(100 * time.Millisecond)
	if closedByServer(conns[0]) || closedByServer(conns[1]) {
		t.Error("connections within the limit were closed")
	}
	if !closedByServer(conns[2]) {
		t.Error("the third connection from one address was kept")
	}
	if h.srv.RejectedPerIPConnections() != 1 {
		t.Errorf("rejected = %d", h.srv.RejectedPerIPConnections())
	}
	// A freed slot can be used again.
	conns[0].Close()
	time.Sleep(100 * time.Millisecond)
	again := dialN(t, h.addr("http"), 1)
	time.Sleep(100 * time.Millisecond)
	if closedByServer(again[0]) {
		t.Error("a connection after one was closed was refused")
	}
}

func TestTrustedProxiesAreNotLimitedPerAddress(t *testing.T) {
	cfg := routedConfig(t, nil, nil)
	cfg.Limits.MaxConnectionsPerIP = 1
	cfg.Sites[0].Limits.MaxConnectionsPerIP = 1
	cfg.TrustedProxies = loopback
	h := start(t, cfg)
	conns := dialN(t, h.addr("http"), 4)
	time.Sleep(100 * time.Millisecond)
	for i, c := range conns {
		if closedByServer(c) {
			t.Errorf("connection %d from a trusted proxy was closed", i)
		}
	}
}

// ---- lock-out after wrong passwords ----

func TestWrongPasswordsLockAClientOut(t *testing.T) {
	ht := htConfig()
	ht.AuthFailures, ht.AuthLockout = 3, model.Duration(1500*time.Millisecond)
	cfg := routedConfig(t, func(s *model.Site) {
		s.HTAccess = ht
		writeFiles(t, s.Root, map[string]string{
			".htaccess": "AuthType Basic\nAuthName x\nAuthUserFile .htpasswd\nRequire valid-user\n", ".htpasswd": htpasswdFile,
		})
	}, nil)
	cfg.TrustedProxies = loopback
	h := start(t, cfg)
	base := "http://" + h.addr("http")
	as := func(ip, user, pass string) *http.Response {
		hdr := basic(user, pass)
		hdr["X-Forwarded-For"] = ip
		resp, _ := do(t, "GET", base+"/hello.txt", hdr, "")
		return resp
	}

	for i := 0; i < 3; i++ {
		if r := as("7.7.7.7", "alice", "wrong"); r.StatusCode != 401 {
			t.Fatalf("wrong password %d: %d", i, r.StatusCode)
		}
	}
	r := as("7.7.7.7", "alice", "secret") // the right password, but the client is locked out
	if r.StatusCode != 429 || r.Header.Get("Retry-After") == "" || r.Header.Get("WWW-Authenticate") != "" {
		t.Errorf("locked out: %d %v", r.StatusCode, r.Header)
	}
	if r := as("7.7.7.8", "alice", "secret"); r.StatusCode != 200 {
		t.Errorf("another client is locked out too: %d", r.StatusCode)
	}
	time.Sleep(1700 * time.Millisecond)
	if r := as("7.7.7.7", "alice", "secret"); r.StatusCode != 200 {
		t.Errorf("after the lock-out: %d", r.StatusCode)
	}
	// A success resets the count: two wrong attempts, a right one, two more are fine.
	as("6.6.6.6", "bob", "x")
	as("6.6.6.6", "bob", "x")
	as("6.6.6.6", "bob", "secret")
	as("6.6.6.6", "bob", "x")
	as("6.6.6.6", "bob", "x")
	if r := as("6.6.6.6", "bob", "secret"); r.StatusCode != 200 {
		t.Errorf("failures before a success counted: %d", r.StatusCode)
	}
	// Without credentials a client is only asked for them: that is not a failure.
	for i := 0; i < 5; i++ {
		resp, _ := do(t, "GET", base+"/hello.txt", fromClient("5.5.5.5"), "")
		if resp.StatusCode != 401 {
			t.Fatalf("anonymous request %d: %d", i, resp.StatusCode)
		}
	}
	if r := as("5.5.5.5", "alice", "secret"); r.StatusCode != 200 {
		t.Errorf("anonymous requests locked a client out: %d", r.StatusCode)
	}
}

func TestLockoutCanBeTurnedOff(t *testing.T) {
	ht := htConfig()
	ht.AuthFailures = 0
	cfg := routedConfig(t, func(s *model.Site) {
		s.HTAccess = ht
		writeFiles(t, s.Root, map[string]string{
			".htaccess": "AuthType Basic\nAuthName x\nAuthUserFile .htpasswd\nRequire valid-user\n", ".htpasswd": htpasswdFile,
		})
	}, nil)
	h := start(t, cfg)
	base := "http://" + h.addr("http")
	for i := 0; i < 20; i++ {
		do(t, "GET", base+"/hello.txt", basic("alice", "wrong"), "")
	}
	if resp, _ := do(t, "GET", base+"/hello.txt", basic("alice", "secret"), ""); resp.StatusCode != 200 {
		t.Errorf("%d", resp.StatusCode)
	}
	_ = strings.TrimSpace
}

func TestInternalRedirectsAreChargedToTheSiteOnce(t *testing.T) {
	routes := []model.Route{
		{Kind: model.RoutePrefix, Path: "/redir/", Rewrites: []model.Rewrite{
			{Pattern: "^/redir/(.*)$", Regexp: mustRe("^/redir/(.*)$"), Target: "/{re.1}", Flag: model.RewriteLast}}},
		{Kind: model.RoutePrefix, Path: "/", Actions: model.Actions{TryFiles: []string{"{uri}", "=404"}}},
	}
	cfg := routedConfig(t, func(s *model.Site) { s.RateLimit = rl(2, time.Hour, 2, model.RateKeyIP) }, routes)
	cfg.TrustedProxies = loopback
	h := start(t, cfg)
	base := "http://" + h.addr("http")
	// Each request is routed twice (the rewrite, then the file), but it is one request.
	for i := 0; i < 2; i++ {
		if resp, _ := do(t, "GET", base+"/redir/hello.txt", fromClient("4.3.2.1"), ""); resp.StatusCode != 200 {
			t.Fatalf("request %d: %d", i+1, resp.StatusCode)
		}
	}
	if resp, _ := do(t, "GET", base+"/redir/hello.txt", fromClient("4.3.2.1"), ""); resp.StatusCode != 429 {
		t.Errorf("third request: %d", resp.StatusCode)
	}
}
