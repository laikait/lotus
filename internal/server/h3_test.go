package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// h3Site is a manual-TLS site on an https listener with HTTP/3, plus the
// certificate a client must trust.
func h3Config(t *testing.T, mutate ...func(*model.Config)) (*model.Config, certFiles) {
	t.Helper()
	cert := validCert(t, "h3.test")
	s := siteConf("h3.test", docroot(t, "hello over h3"), "https")
	s.TLS, s.Certificate, s.CertificateKey = model.TLSManual, cert.certPath, cert.keyPath
	cfg := baseConfig(s)
	cfg.TLS.MinVersion = "1.3"
	cfg.Listeners = []model.Listener{{Name: "https", Address: "127.0.0.1:0", HTTPS: true, HTTP3: true}}
	for _, m := range mutate {
		m(cfg)
	}
	return cfg, cert
}

// h3Client is a client that speaks HTTP/3 only, to the UDP port with the same
// number as the listener's TCP port. Each client has its own QUIC connection.
func h3Client(t *testing.T, h *harness, cert certFiles) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(cert.cert)
	target := h.addr("https")
	tr := &http3.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "h3.test"},
		Dial: func(ctx context.Context, _ string, tlsCfg *tls.Config, cfg *quic.Config) (*quic.Conn, error) {
			return quic.DialAddr(ctx, target, tlsCfg, cfg)
		},
	}
	t.Cleanup(func() { tr.Close() })
	return &http.Client{Transport: tr, Timeout: 5 * time.Second}
}

// setLimits changes the server's limits, and the sites' copies of them (a site
// can't differ from the server except in max_request_body).
func setLimits(c *model.Config, f func(*model.Limits)) {
	f(&c.Limits)
	for i := range c.Sites {
		body := c.Sites[i].Limits.MaxRequestBody
		c.Sites[i].Limits = c.Limits
		c.Sites[i].Limits.MaxRequestBody = body
	}
}

func h3Get(t *testing.T, c *http.Client, path string) (*http.Response, string, error) {
	t.Helper()
	resp, err := c.Get("https://h3.test" + path)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b), nil
}

func TestHTTP3ServesTheSites(t *testing.T) {
	cfg, cert := h3Config(t, func(c *model.Config) {
		c.Sites[0].SecurityHeaders = []model.Header{{Name: "X-Frame-Options", Value: "DENY"}}
	})
	h := start(t, cfg)
	resp, body, err := h3Get(t, h3Client(t, h, cert), "/")
	if err != nil {
		t.Fatal(err)
	}
	if resp.ProtoMajor != 3 || resp.StatusCode != 200 || body != "hello over h3" {
		t.Errorf("%s %d %q", resp.Proto, resp.StatusCode, body)
	}
	if resp.Header.Get("Server") != "lotus" || resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Errorf("headers: %v", resp.Header)
	}
	if resp.Header.Get("Alt-Svc") != "" {
		t.Errorf("Alt-Svc on an HTTP/3 response: %q", resp.Header.Get("Alt-Svc"))
	}
	if _, _, err := h3Get(t, h3Client(t, h, cert), "/missing"); err != nil {
		t.Error(err)
	}
	// The access log shows the protocol.
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(h.stdout.String(), `HTTP/3.0" 200`) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(h.stdout.String(), `HTTP/3.0" 200`) {
		t.Errorf("access log: %q", h.stdout.String())
	}
}

func TestHTTP3IsAdvertisedOnTCPResponsesOnly(t *testing.T) {
	cfg, cert := h3Config(t)
	h := start(t, cfg)
	port := h.srv.Addrs()["https"].(*net.TCPAddr).Port

	pool := x509.NewCertPool()
	pool.AddCert(cert.cert)
	tcp := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "h3.test"}, ForceAttemptHTTP2: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", h.addr("https"))
		},
	}}
	resp, err := tcp.Get("https://h3.test/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if want := fmt.Sprintf(`h3=":%d"; ma=86400`, port); resp.Header.Get("Alt-Svc") != want {
		t.Errorf("Alt-Svc = %q, want %q", resp.Header.Get("Alt-Svc"), want)
	}
	if resp.ProtoMajor != 2 {
		t.Errorf("TCP clients still get %s", resp.Proto)
	}
}

func TestNothingIsAdvertisedWithoutHTTP3(t *testing.T) {
	cfg, cert := h3Config(t, func(c *model.Config) { c.Listeners[0].HTTP3 = false })
	h := start(t, cfg)
	pool := x509.NewCertPool()
	pool.AddCert(cert.cert)
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "h3.test"},
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", h.addr("https"))
		},
	}}
	resp, err := c.Get("https://h3.test/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Alt-Svc") != "" {
		t.Errorf("Alt-Svc = %q", resp.Header.Get("Alt-Svc"))
	}
	// And no UDP port is bound: the port can be taken by someone else.
	pc, err := net.ListenPacket("udp", h.addr("https"))
	if err != nil {
		t.Errorf("the UDP port is in use: %v", err)
	} else {
		pc.Close()
	}
}

func TestHTTP3HostMustMatchTheHandshakeName(t *testing.T) {
	cert := validCert(t, "h3.test", "other.test")
	a := siteConf("h3.test", docroot(t, "site a"), "https")
	b := siteConf("other.test", docroot(t, "site b"), "https")
	for _, s := range []*model.Site{&a, &b} {
		s.TLS, s.Certificate, s.CertificateKey = model.TLSManual, cert.certPath, cert.keyPath
	}
	cfg := baseConfig(a, b)
	cfg.Listeners = []model.Listener{{Name: "https", Address: "127.0.0.1:0", HTTPS: true, HTTP3: true}}
	h := start(t, cfg)
	c := h3Client(t, h, cert) // handshake name h3.test

	req, _ := http.NewRequest("GET", "https://h3.test/", nil)
	req.Host = "other.test"
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("a connection for one site reached another over HTTP/3: %d", resp.StatusCode)
	}
}

func TestHTTP3HonoursConnectionLimits(t *testing.T) {
	cfg, cert := h3Config(t, func(c *model.Config) { setLimits(c, func(l *model.Limits) { l.MaxConnectionsPerIP = 1 }) })
	h := start(t, cfg)

	first := h3Client(t, h, cert)
	if _, _, err := h3Get(t, first, "/"); err != nil {
		t.Fatal(err)
	}
	// A second connection from the same address is refused.
	second := h3Client(t, h, cert)
	if _, _, err := h3Get(t, second, "/"); err == nil {
		t.Error("a second QUIC connection from the same address was served")
	}
	if n := h.srv.RejectedPerIPConnections(); n == 0 {
		t.Error("the refused connection was not counted")
	}
	// The first connection still works.
	if _, _, err := h3Get(t, first, "/"); err != nil {
		t.Errorf("the connection within the limit broke: %v", err)
	}
}

func TestHTTP3GlobalConnectionLimitReleasesSlots(t *testing.T) {
	cfg, cert := h3Config(t, func(c *model.Config) { setLimits(c, func(l *model.Limits) { l.MaxConnections = 1 }) })
	h := start(t, cfg)
	for i := 0; i < 4; i++ {
		// One connection at a time; each is closed before the next, so each must
		// find the slot free again.
		c := h3Client(t, h, cert)
		if _, _, err := h3Get(t, c, "/"); err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		c.Transport.(*http3.Transport).Close()
		time.Sleep(100 * time.Millisecond)
	}
}

func TestHTTP3HeaderLimit(t *testing.T) {
	cfg, cert := h3Config(t, func(c *model.Config) { setLimits(c, func(l *model.Limits) { l.MaxHeaderBytes = 2 << 10 }) })
	h := start(t, cfg)
	req, _ := http.NewRequest("GET", "https://h3.test/", nil)
	req.Header.Set("X-Big", strings.Repeat("a", 16<<10))
	resp, err := h3Client(t, h, cert).Do(req)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
			t.Errorf("a 16 KiB header with max_header_bytes 2kib got %d", resp.StatusCode)
		}
	}
	// Ordinary requests are not affected.
	if _, _, err := h3Get(t, h3Client(t, h, cert), "/"); err != nil {
		t.Error(err)
	}
}

// bodyBackend answers with the number of body bytes it received.
func bodyBackend(t *testing.T) *httptest.Server {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		fmt.Fprintf(w, "%d %v", n, err)
	}))
	t.Cleanup(be.Close)
	return be
}

func TestHTTP3RequestBodyLimit(t *testing.T) {
	be := bodyBackend(t)
	cfg, cert := h3Config(t, func(c *model.Config) {
		s := &c.Sites[0]
		s.Limits.MaxRequestBody = 1 << 10
		c.Upstreams = []model.Upstream{upstreamOf("app", be.URL)}
		r := proxyRoute("/", model.Actions{Proxy: "app"})
		r.MaxRequestBody = 1 << 10
		s.Routes = []model.Route{r}
	})
	h := start(t, cfg)
	c := h3Client(t, h, cert)
	resp, err := c.Post("https://h3.test/", "text/plain", strings.NewReader(strings.Repeat("x", 64<<10)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("a 64 KiB body with a 1 KiB limit got %d over HTTP/3", resp.StatusCode)
	}
	resp, err = c.Post("https://h3.test/", "text/plain", strings.NewReader("small"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(string(b), "5 ") {
		t.Errorf("a small body: %d %q", resp.StatusCode, b)
	}
}

func TestHTTP3ReadTimeoutAppliesToSlowUploads(t *testing.T) {
	be := bodyBackend(t)
	cfg, cert := h3Config(t, func(c *model.Config) {
		setLimits(c, func(l *model.Limits) { l.ReadTimeout = model.Duration(400 * time.Millisecond) })
		c.Upstreams = []model.Upstream{upstreamOf("app", be.URL)}
		r := proxyRoute("/", model.Actions{Proxy: "app"})
		r.MaxRequestBody = model.DefaultMaxRequestBody
		c.Sites[0].Routes = []model.Route{r}
	})
	h := start(t, cfg)
	c := h3Client(t, h, cert)
	pr, pw := io.Pipe()
	go func() {
		pw.Write([]byte("start"))
		time.Sleep(2 * time.Second) // a client that stops sending
		pw.Close()
	}()
	begin := time.Now()
	resp, err := c.Post("https://h3.test/", "text/plain", pr)
	if err == nil {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.HasPrefix(string(b), "5 <nil>") {
			t.Errorf("a stalled upload completed (%d %q)", resp.StatusCode, b)
		}
	}
	if time.Since(begin) > 1500*time.Millisecond {
		t.Errorf("the stalled upload held the request for %v with read_timeout 400ms", time.Since(begin))
	}
}

func TestHTTP3ThroughTheProxy(t *testing.T) {
	var mu sync.Mutex // the backend handler runs in another goroutine
	var seen http.Header
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Alt-Svc", `h3="evil.example:443"`)
		w.Header().Set("Server", "backend")
		io.WriteString(w, "from the backend")
	}))
	defer be.Close()
	cfg, cert := h3Config(t, func(c *model.Config) {
		c.Upstreams = []model.Upstream{upstreamOf("app", be.URL)}
		c.Sites[0].Routes = []model.Route{proxyRoute("/", model.Actions{Proxy: "app"})}
	})
	h := start(t, cfg)
	resp, body, err := h3Get(t, h3Client(t, h, cert), "/x")
	if err != nil {
		t.Fatal(err)
	}
	if body != "from the backend" || resp.ProtoMajor != 3 {
		t.Errorf("%s %q", resp.Proto, body)
	}
	if resp.Header.Get("Alt-Svc") != "" || resp.Header.Get("Server") != "lotus" {
		t.Errorf("the backend's Alt-Svc or Server reached the client: %v", resp.Header)
	}
	mu.Lock()
	defer mu.Unlock()
	if seen.Get("X-Forwarded-Proto") != "https" || !strings.HasPrefix(seen.Get("X-Forwarded-For"), "127.0.0.1") {
		t.Errorf("forwarding headers over HTTP/3: %v", seen)
	}
}

func TestHTTP3SurvivesAReload(t *testing.T) {
	cfg, cert := h3Config(t)
	h := start(t, cfg)
	c := h3Client(t, h, cert)
	if _, _, err := h3Get(t, c, "/"); err != nil {
		t.Fatal(err)
	}
	cfg2, _ := h3Config(t)
	cfg2.Sites[0].Certificate, cfg2.Sites[0].CertificateKey = cert.certPath, cert.keyPath
	cfg2.Sites[0].Root = cfg.Sites[0].Root
	cfg2.Sites[0].Routes = []model.Route{{Kind: model.RoutePrefix, Path: "/", Root: cfg.Sites[0].Root, Index: cfg.Sites[0].Index,
		MaxRequestBody: model.DefaultMaxRequestBody, Actions: ret(200, "reloaded")}}
	if err := h.srv.Reload(cfg2); err != nil {
		t.Fatal(err)
	}
	if _, body, err := h3Get(t, c, "/"); err != nil || strings.TrimSpace(body) != "reloaded" {
		t.Errorf("after the reload: %q %v", body, err)
	}
	// Turning HTTP/3 on or off needs a restart.
	cfg3, _ := h3Config(t, func(c *model.Config) { c.Listeners[0].HTTP3 = false })
	if err := h.srv.Reload(cfg3); err == nil || !strings.Contains(err.Error(), "restart") {
		t.Errorf("err = %v", err)
	}
}

func TestHTTP3ShutdownReleasesThePort(t *testing.T) {
	cfg, cert := h3Config(t)
	h := start(t, cfg)
	addr := h.addr("https")
	c := h3Client(t, h, cert)
	if _, _, err := h3Get(t, c, "/"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	h.srv.Shutdown(ctx)
	if time.Since(start) > 4*time.Second {
		t.Errorf("shutdown took %v", time.Since(start))
	}
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		t.Fatalf("the UDP port was not released: %v", err)
	}
	pc.Close()
}

func TestAnUnavailableUDPPortStopsTheStart(t *testing.T) {
	busy, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	cfg, _ := h3Config(t, func(c *model.Config) { c.Listeners[0].Address = busy.LocalAddr().String() })
	srv, err := New(cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	err = srv.Start()
	if err == nil || !strings.Contains(err.Error(), "UDP") {
		t.Fatalf("err = %v", err)
	}
	// Nothing was left bound: the TCP port is free again.
	ln, err := net.Listen("tcp", busy.LocalAddr().String())
	if err != nil {
		t.Errorf("the TCP listener was left open: %v", err)
	} else {
		ln.Close()
	}
}
