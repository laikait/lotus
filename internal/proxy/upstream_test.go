package proxy

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// backendServer starts a server that answers with its own name.
func backendServer(t *testing.T, name string, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	if h == nil {
		h = func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, name) }
	}
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func spec(s *httptest.Server, weight int) BackendSpec {
	return BackendSpec{Network: "tcp", Address: strings.TrimPrefix(s.URL, "http://"), Weight: weight}
}

func newUp(t *testing.T, cfg Config) *Upstream {
	t.Helper()
	if cfg.Name == "" {
		cfg.Name = "test"
	}
	u, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(u.Close)
	return u
}

func get(t *testing.T, u *Upstream, method, path string, body io.Reader, hdr map[string]string) (int, string, error) {
	t.Helper()
	req, _ := http.NewRequest(method, "http://placeholder"+path, body)
	req.Host = "client.example"
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := u.RoundTrip(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), nil
}

func counts(t *testing.T, u *Upstream, n int) map[string]int {
	t.Helper()
	m := map[string]int{}
	for i := 0; i < n; i++ {
		_, body, err := get(t, u, "GET", "/", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		m[body]++
	}
	return m
}

func TestRoundRobinFollowsWeightsSmoothly(t *testing.T) {
	a, b, c := backendServer(t, "a", nil), backendServer(t, "b", nil), backendServer(t, "c", nil)
	u := newUp(t, Config{Backends: []BackendSpec{spec(a, 5), spec(b, 1), spec(c, 1)}})
	var seq []string
	for i := 0; i < 7; i++ {
		_, body, _ := get(t, u, "GET", "/", nil, nil)
		seq = append(seq, body)
	}
	if got := strings.Join(seq, ""); got != "aabacaa" {
		t.Errorf("sequence %q, want the smooth weighted order aabacaa", got)
	}
	if m := counts(t, u, 70); m["a"] != 50 || m["b"] != 10 || m["c"] != 10 {
		t.Errorf("distribution %v", m)
	}
}

func TestLeastConnPrefersTheIdleBackend(t *testing.T) {
	release := make(chan struct{})
	var started atomic.Int32
	slow := backendServer(t, "slow", func(w http.ResponseWriter, r *http.Request) {
		started.Add(1)
		<-release
		io.WriteString(w, "slow")
	})
	fast := backendServer(t, "fast", nil)
	u := newUp(t, Config{Balance: LeastConn, Backends: []BackendSpec{spec(slow, 1), spec(fast, 1)}})
	// Idle backends tie, so some of these requests land on the slow one and
	// stay there; the fast one answers at once.
	var wg sync.WaitGroup
	var once sync.Once
	releaseAll := func() { once.Do(func() { close(release) }) }
	defer func() { releaseAll(); wg.Wait() }()
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			get(t, u, "GET", "/hold", nil, nil)
		}()
	}
	// least_conn alternates while loads are equal, so wait until slow has some.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && u.backends[0].state.Load() < 1 {
		time.Sleep(10 * time.Millisecond)
	}
	if u.backends[0].state.Load() < 1 {
		t.Fatal("the slow backend never got a request")
	}
	m := map[string]int{}
	for i := 0; i < 10; i++ {
		_, body, _ := get(t, u, "GET", "/", nil, nil)
		m[body]++
	}
	releaseAll()
	wg.Wait()
	if m["fast"] < 10 {
		t.Errorf("least_conn sent %v while one backend was busy", m)
	}
}

func TestHashBalancersAreStickyAndMoveOnlyTheLostKeys(t *testing.T) {
	names := []string{"a", "b", "c", "d"}
	var specs []BackendSpec
	var servers []*httptest.Server
	for _, n := range names {
		s := backendServer(t, n, nil)
		servers = append(servers, s)
		specs = append(specs, spec(s, 1))
	}
	u := newUp(t, Config{Balance: IPHash, Backends: specs})
	assign := func() map[string]string {
		m := map[string]string{}
		for i := 0; i < 60; i++ {
			ip := "203.0.113." + string(rune('0'+i/10)) + string(rune('0'+i%10))
			_, body, err := get(t, u, "GET", "/", nil, map[string]string{"X-Forwarded-For": ip})
			if err != nil {
				t.Fatal(err)
			}
			m[ip] = body
		}
		return m
	}
	before, again := assign(), assign()
	for ip, who := range before {
		if again[ip] != who {
			t.Fatalf("%s moved from %s to %s with nothing changed", ip, who, again[ip])
		}
	}
	seen := map[string]bool{}
	for _, who := range before {
		seen[who] = true
	}
	if len(seen) < 3 {
		t.Errorf("60 clients used only %v", seen)
	}
	// Take "b" out: only its clients may move.
	u.backends[1].probeDown.Store(true)
	after := assign()
	for ip, who := range before {
		if who != "b" && after[ip] != who {
			t.Errorf("%s was on %s and moved to %s although %s is the one that left", ip, who, after[ip], "b")
		}
		if after[ip] == "b" {
			t.Errorf("%s still goes to the backend that is down", ip)
		}
	}
	_ = servers

	u2 := newUp(t, Config{Balance: URIHash, Backends: specs})
	_, first, _ := get(t, u2, "GET", "/same/path", nil, nil)
	for i := 0; i < 10; i++ {
		if _, body, _ := get(t, u2, "GET", "/same/path", nil, nil); body != first {
			t.Fatalf("uri_hash moved /same/path from %s to %s", first, body)
		}
	}
}

func TestRandomUsesEveryBackend(t *testing.T) {
	a, b := backendServer(t, "a", nil), backendServer(t, "b", nil)
	u := newUp(t, Config{Balance: Random, Backends: []BackendSpec{spec(a, 1), spec(b, 1)}})
	if m := counts(t, u, 60); m["a"] < 10 || m["b"] < 10 {
		t.Errorf("%v", m)
	}
}

// deadAddr returns an address nothing listens on.
func deadAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func TestFailedConnectionsMoveToAnotherBackend(t *testing.T) {
	good := backendServer(t, "good", nil)
	u := newUp(t, Config{Retries: 1, Backends: []BackendSpec{{Network: "tcp", Address: deadAddr(t)}, spec(good, 1)}})
	for i := 0; i < 6; i++ {
		// A POST with a body is safe to repeat here: the connection was never made.
		code, body, err := get(t, u, "POST", "/", strings.NewReader("payload"), nil)
		if err != nil || code != 200 || body != "good" {
			t.Fatalf("request %d: %d %q %v", i, code, body, err)
		}
	}
}

func TestNoRetryWithoutRetries(t *testing.T) {
	good := backendServer(t, "good", nil)
	u := newUp(t, Config{Retries: 0, Backends: []BackendSpec{{Network: "tcp", Address: deadAddr(t)}, spec(good, 1)}})
	if _, _, err := get(t, u, "GET", "/", nil, nil); err == nil {
		t.Error("the first request went to the dead backend and should have failed")
	}
}

func TestPostIsNotRepeatedAfterTheBackendReadIt(t *testing.T) {
	var hits atomic.Int32
	reset := func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.Copy(io.Discard, r.Body)
		hj, _ := w.(http.Hijacker)
		c, _, _ := hj.Hijack()
		c.Close() // reset after the body was consumed
	}
	a := backendServer(t, "a", reset)
	b := backendServer(t, "b", reset)
	u := newUp(t, Config{Retries: 2, Backends: []BackendSpec{spec(a, 1), spec(b, 1)}})
	if _, _, err := get(t, u, "POST", "/", strings.NewReader("do it once"), nil); err == nil {
		t.Fatal("expected an error")
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("a POST whose body was read reached the backends %d times", n)
	}
	hits.Store(0)
	// A GET is idempotent: it may go to the next backend.
	if _, _, err := get(t, u, "GET", "/", nil, nil); err == nil {
		t.Fatal("expected an error")
	}
	if n := hits.Load(); n < 2 {
		t.Errorf("an idempotent GET was not tried on another backend (%d hits)", n)
	}
}

func TestSlowBackendIsATimeoutAndIsNotRepeated(t *testing.T) {
	var hits atomic.Int32
	slow := func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(400 * time.Millisecond)
	}
	a, b := backendServer(t, "a", slow), backendServer(t, "b", slow)
	u := newUp(t, Config{Retries: 2, ResponseTimeout: 100 * time.Millisecond, Backends: []BackendSpec{spec(a, 1), spec(b, 1)}})
	_, _, err := get(t, u, "GET", "/", nil, nil)
	if err == nil || !isTimeout(err) {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if hits.Load() != 1 {
		t.Errorf("a timed-out request was sent %d times", hits.Load())
	}
}

func TestPassiveFailureTakesABackendOutAndItComesBack(t *testing.T) {
	var broken atomic.Bool
	broken.Store(true)
	flaky := backendServer(t, "flaky", func(w http.ResponseWriter, r *http.Request) {
		if broken.Load() {
			hj, _ := w.(http.Hijacker)
			c, _, _ := hj.Hijack()
			c.Close()
			return
		}
		io.WriteString(w, "flaky")
	})
	good := backendServer(t, "good", nil)
	u := newUp(t, Config{Retries: 1, MaxFails: 2, FailTimeout: 300 * time.Millisecond, Backends: []BackendSpec{spec(flaky, 1), spec(good, 1)}})
	for i := 0; i < 8; i++ {
		get(t, u, "GET", "/", nil, nil)
	}
	if u.backends[0].usable(time.Now()) {
		t.Fatal("a backend that kept failing is still in rotation")
	}
	for _, st := range u.Status() {
		if st.Address == u.backends[0].spec.Address && st.Healthy {
			t.Error("Status says it's healthy")
		}
	}
	for i := 0; i < 5; i++ {
		if _, body, err := get(t, u, "GET", "/", nil, nil); err != nil || body != "good" {
			t.Fatalf("while it is out: %q %v", body, err)
		}
	}
	broken.Store(false)
	time.Sleep(350 * time.Millisecond)
	m := counts(t, u, 20)
	if m["flaky"] == 0 {
		t.Errorf("a repaired backend never came back: %v", m)
	}
}

func TestAllBackendsDown(t *testing.T) {
	u := newUp(t, Config{MaxFails: 1, FailTimeout: time.Minute, Backends: []BackendSpec{{Network: "tcp", Address: deadAddr(t)}}})
	get(t, u, "GET", "/", nil, nil) // fails and takes it out
	if _, _, err := get(t, u, "GET", "/", nil, nil); !errors.Is(err, ErrNoBackend) {
		t.Errorf("err = %v, want ErrNoBackend", err)
	}
}

func TestActiveHealthCheckHTTP(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(true)
	var probes atomic.Int32
	a := backendServer(t, "a", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			probes.Add(1)
			if r.Header.Get("User-Agent") != "lotus-health-check" {
				t.Errorf("probe User-Agent %q", r.Header.Get("User-Agent"))
			}
			if !healthy.Load() {
				http.Error(w, "sick", 500)
			}
			return
		}
		io.WriteString(w, "a")
	})
	b := backendServer(t, "b", nil)
	u := newUp(t, Config{Backends: []BackendSpec{spec(a, 1), spec(b, 1)},
		Health: &HealthSpec{Path: "/health", Interval: 40 * time.Millisecond, Timeout: time.Second, Healthy: 2, Unhealthy: 2}})
	waitFor(t, "probes", func() bool { return probes.Load() >= 2 })
	healthy.Store(false)
	waitFor(t, "a to be marked down", func() bool { return !u.backends[0].usable(time.Now()) })
	if m := counts(t, u, 10); m["a"] != 0 {
		t.Errorf("traffic still goes to the sick backend: %v", m)
	}
	healthy.Store(true)
	waitFor(t, "a to recover", func() bool { return u.backends[0].usable(time.Now()) })
}

func TestActiveHealthCheckTCP(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	u := newUp(t, Config{Backends: []BackendSpec{{Network: "tcp", Address: addr}},
		Health: &HealthSpec{Interval: 30 * time.Millisecond, Timeout: 200 * time.Millisecond, Healthy: 1, Unhealthy: 2}})
	time.Sleep(150 * time.Millisecond)
	if !u.backends[0].usable(time.Now()) {
		t.Fatal("a listening backend was marked down")
	}
	l.Close()
	waitFor(t, "the closed port to be marked down", func() bool { return !u.backends[0].usable(time.Now()) })
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestConnectionLimits(t *testing.T) {
	release := make(chan struct{})
	a := backendServer(t, "a", func(w http.ResponseWriter, r *http.Request) { <-release; io.WriteString(w, "a") })
	one := spec(a, 1)
	one.MaxConns = 1
	u := newUp(t, Config{Backends: []BackendSpec{one}})
	done := make(chan struct{})
	go func() { get(t, u, "GET", "/", nil, nil); close(done) }()
	waitFor(t, "the first request to be in flight", func() bool { return u.backends[0].state.Load() == 1 })
	if _, _, err := get(t, u, "GET", "/", nil, nil); !errors.Is(err, ErrBusy) {
		t.Errorf("err = %v, want ErrBusy", err)
	}
	close(release)
	<-done
	waitFor(t, "the slot to be released", func() bool { return u.backends[0].state.Load() == 0 })
	if code, _, err := get(t, u, "GET", "/", nil, nil); err != nil || code != 200 {
		t.Errorf("after release: %d %v", code, err)
	}

	// The whole upstream can be limited too.
	release2 := make(chan struct{})
	b := backendServer(t, "b", func(w http.ResponseWriter, r *http.Request) { <-release2 })
	u2 := newUp(t, Config{MaxConns: 1, Backends: []BackendSpec{spec(b, 1)}})
	done2 := make(chan struct{})
	go func() { get(t, u2, "GET", "/", nil, nil); close(done2) }()
	waitFor(t, "in flight", func() bool { return u2.active.Load() == 1 })
	if _, _, err := get(t, u2, "GET", "/", nil, nil); !errors.Is(err, ErrBusy) {
		t.Errorf("upstream limit: %v", err)
	}
	close(release2)
	<-done2
}

func TestHostHeader(t *testing.T) {
	var got atomic.Value
	a := backendServer(t, "a", func(w http.ResponseWriter, r *http.Request) { got.Store(r.Host) })
	u := newUp(t, Config{Backends: []BackendSpec{spec(a, 1)}})
	get(t, u, "GET", "/", nil, nil)
	if got.Load() != "client.example" {
		t.Errorf("the client's Host is kept: %v", got.Load())
	}
	req, _ := http.NewRequest("GET", "http://placeholder/", nil)
	req.Host = "" // the server asks for the backend's own host
	resp, err := u.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got.Load() != strings.TrimPrefix(a.URL, "http://") {
		t.Errorf("empty Host becomes the backend's address: %v", got.Load())
	}
}

func TestUnixSocketBackend(t *testing.T) {
	dir, err := os.MkdirTemp("", "lp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip("no unix sockets here:", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "unix:"+r.Host) })}
	go srv.Serve(l)
	defer srv.Close()
	u := newUp(t, Config{Backends: []BackendSpec{{Network: "unix", Address: sock}}})
	_, body, err := get(t, u, "GET", "/", nil, nil)
	if err != nil || body != "unix:client.example" {
		t.Errorf("%q %v", body, err)
	}
}

// ---- TLS ----

func tlsBackend(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "secure") }))
	s.EnableHTTP2 = true
	s.StartTLS()
	t.Cleanup(s.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return s, ca
}

func TestUpstreamTLSIsVerifiedByDefault(t *testing.T) {
	s, ca := tlsBackend(t)
	addr := strings.TrimPrefix(s.URL, "https://")
	be := func() []BackendSpec { return []BackendSpec{{Network: "tcp", Address: addr, TLS: true}} }

	u := newUp(t, Config{Backends: be(), TLS: TLSSpec{Verify: true}})
	if _, _, err := get(t, u, "GET", "/", nil, nil); err == nil {
		t.Error("a certificate nobody trusts was accepted")
	}
	u = newUp(t, Config{Backends: be(), TLS: TLSSpec{Verify: true, CAFile: ca}})
	if code, body, err := get(t, u, "GET", "/", nil, nil); err != nil || code != 200 || body != "secure" {
		t.Errorf("with the CA: %d %q %v", code, body, err)
	}
	// httptest certificates are for example.com, not 127.0.0.1: the name is checked.
	u = newUp(t, Config{Backends: be(), TLS: TLSSpec{Verify: true, CAFile: ca, ServerName: "wrong.example"}})
	if _, _, err := get(t, u, "GET", "/", nil, nil); err == nil {
		t.Error("a certificate for another name was accepted")
	}
	u = newUp(t, Config{Backends: be(), TLS: TLSSpec{Verify: false}})
	if code, _, err := get(t, u, "GET", "/", nil, nil); err != nil || code != 200 {
		t.Errorf("verification off: %d %v", code, err)
	}
}

func TestBadTLSFilesAreRefusedAtStart(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "x.pem")
	os.WriteFile(bad, []byte("not a certificate"), 0o600)
	for name, ts := range map[string]TLSSpec{
		"missing ca": {Verify: true, CAFile: bad + ".nope"}, "junk ca": {Verify: true, CAFile: bad},
		"junk client cert": {Verify: true, CertFile: bad, KeyFile: bad},
	} {
		if _, err := New(Config{Name: "x", Backends: []BackendSpec{{Network: "tcp", Address: "127.0.0.1:1"}}, TLS: ts}, nil); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := New(Config{Name: "x"}, nil); err == nil {
		t.Error("an upstream without backends")
	}
}

func TestClientGoingAwayDoesNotCountAgainstTheBackend(t *testing.T) {
	a := backendServer(t, "a", func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) })
	u := newUp(t, Config{MaxFails: 1, FailTimeout: time.Minute, Backends: []BackendSpec{spec(a, 1)}})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://x/", nil)
	if _, err := u.RoundTrip(req); err == nil {
		t.Fatal("expected a cancellation")
	}
	if !u.backends[0].usable(time.Now()) {
		t.Error("a client that gave up took the backend out of rotation")
	}
	var _ tls.Config
}
