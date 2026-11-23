package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/config/model"
)

func TestUnsupportedFeaturesFailLoudly(t *testing.T) {
	root := docroot(t, "x")
	tests := []struct {
		name   string
		mutate func(*model.Config)
		want   string
	}{
		{"php in a route", func(c *model.Config) {
			c.Sites[0].Routes = []model.Route{{Kind: model.RoutePrefix, Path: "/", Actions: model.Actions{PHP: "main"}}}
		}, "php"},
		{"php in a nested route", func(c *model.Config) {
			c.Sites[0].Routes = []model.Route{{Kind: model.RoutePrefix, Path: "/", Children: []model.Route{
				{Kind: model.RoutePrefix, Path: "/a/", Actions: model.Actions{PHP: "main"}}}}}
		}, "php"},
		{"php in a named route", func(c *model.Config) {
			c.Sites[0].Named = []model.Route{{Kind: model.RouteNamed, Path: "@x", Actions: model.Actions{PHP: "main"}}}
		}, "php"},
		{"php in a match block", func(c *model.Config) {
			c.Sites[0].Routes = []model.Route{{Kind: model.RoutePrefix, Path: "/",
				Matches: []model.Match{{Methods: []string{"POST"}, Actions: model.Actions{PHP: "main"}}}}}
		}, "php"},
		{"acme", func(c *model.Config) { c.Sites[0].TLS = model.TLSAutomatic }, "automatic certificates"},
		{"http3", func(c *model.Config) { c.Listeners[0].HTTP3 = true }, "http3"},
		{"control", func(c *model.Config) { c.Control.Socket = "/tmp/x.sock" }, "control"},
		{"per-site timeout", func(c *model.Config) { c.Sites[0].Limits.IdleTimeout = model.Duration(time.Second) }, "server-wide"},
		{"per-site connection limit", func(c *model.Config) { c.Sites[0].Limits.MaxConnections = 5 }, "server-wide"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig(siteConf("a.test", root, "http"))
			tt.mutate(cfg)
			_, err := New(cfg, Options{})
			var ue *UnsupportedError
			if !asUnsupported(err, &ue) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want an UnsupportedError mentioning %q", err, tt.want)
			}
		})
	}

	t.Run("a per-site max_request_body is supported", func(t *testing.T) {
		cfg := baseConfig(siteConf("a.test", root, "http"))
		cfg.Sites[0].Limits.MaxRequestBody = 1 << 10
		if items := Unsupported(cfg); len(items) != 0 {
			t.Errorf("unexpected: %v", items)
		}
	})
	t.Run("all problems are listed together", func(t *testing.T) {
		cfg := baseConfig(siteConf("a.test", root, "http"))
		cfg.Listeners[0].HTTP3 = true
		cfg.Sites[0].TLS = model.TLSAutomatic
		cfg.Sites[0].Routes = []model.Route{{Kind: model.RoutePrefix, Path: "/", Actions: model.Actions{PHP: "main"}}}
		if items := Unsupported(cfg); len(items) != 3 {
			t.Errorf("got %d items: %v", len(items), items)
		}
	})
}

func TestVirtualHosts(t *testing.T) {
	a, b, def := docroot(t, "site-a"), docroot(t, "site-b"), docroot(t, "site-default")
	sa := siteConf("a.test", a, "http")
	sa.Aliases = []string{"www.a.test", "*.a.test"}
	h := start(t, baseConfig(sa, siteConf("b.test", b, "http"), siteConf("default.test", def, "http")))
	base := "http://" + h.addr("http")

	tests := []struct {
		host, want string
	}{
		{"a.test", "site-a"},
		{"WWW.A.TEST", "site-a"},
		{"a.test:8080", "site-a"}, // a port in the Host header is ignored
		{"shop.a.test", "site-a"}, // wildcard alias
		{"b.test", "site-b"},
		{"unknown.test", "site-a"},     // default: the first site on the listener
		{"a.test.evil.test", "site-a"}, // not a match, so also the default
	}
	for _, tt := range tests {
		resp, body := getHost(t, base+"/", tt.host)
		if resp.StatusCode != 200 || body != tt.want {
			t.Errorf("Host %q: %d %q, want 200 %q", tt.host, resp.StatusCode, body, tt.want)
		}
	}
	for _, bad := range []string{"a b.test", "a/b", "a.test/../x", "ex@mple.test"} {
		resp, _ := getHost(t, base+"/", bad)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("Host %q: %d, want 400", bad, resp.StatusCode)
		}
	}
}

func TestSitesOnlyAnswerOnTheirListeners(t *testing.T) {
	cfg := baseConfig(siteConf("a.test", docroot(t, "site-a"), "one"), siteConf("b.test", docroot(t, "site-b"), "two"))
	cfg.Listeners = []model.Listener{{Name: "one", Address: "127.0.0.1:0"}, {Name: "two", Address: "127.0.0.1:0"}}
	h := start(t, cfg)

	// Asking listener "one" for b.test falls back to listener one's default (a.test), never to b's content.
	_, body := getHost(t, "http://"+h.addr("one")+"/", "b.test")
	if body != "site-a" {
		t.Errorf("listener one served %q for b.test", body)
	}
	_, body = getHost(t, "http://"+h.addr("two")+"/", "a.test")
	if body != "site-b" {
		t.Errorf("listener two served %q for a.test", body)
	}
}

func TestResponseHeaders(t *testing.T) {
	s := siteConf("a.test", docroot(t, "x"), "http")
	s.SecurityHeaders = []model.Header{{Name: "X-Frame-Options", Value: "DENY"}, {Name: "x-content-type-options", Value: "nosniff"}}
	h := start(t, baseConfig(s))
	for _, path := range []string{"/", "/missing", "/.env"} { // headers must be on error responses too
		resp, _ := getHost(t, "http://"+h.addr("http")+path, "a.test")
		if resp.Header.Get("X-Frame-Options") != "DENY" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: security headers missing: %v", path, resp.Header)
		}
		if got := resp.Header.Get("Server"); got != "lotus" {
			t.Errorf("%s: Server = %q; it must name the product and nothing more", path, got)
		}
	}
}

func TestStaticBehaviorThroughServer(t *testing.T) {
	h := start(t, baseConfig(siteConf("a.test", docroot(t, "home"), "http")))
	base := "http://" + h.addr("http")
	for path, want := range map[string]int{"/": 200, "/hello.txt": 200, "/missing": 404, "/.env": 403, "/../x": 400} {
		resp, _ := getHost(t, base+path, "a.test")
		if resp.StatusCode != want {
			// Go's client cleans "/../x" to "/x", which is simply missing.
			if path == "/../x" && resp.StatusCode == 404 {
				continue
			}
			t.Errorf("GET %s = %d, want %d", path, resp.StatusCode, want)
		}
	}
	resp, _ := getHost(t, base+"/", "a.test")
	if resp.Header.Get("Last-Modified") == "" || resp.Header.Get("Etag") == "" {
		t.Error("static responses must carry validators")
	}
}

func TestSiteWithoutDocumentRoot(t *testing.T) {
	h := start(t, baseConfig(siteConf("a.test", "", "http")))
	resp, _ := getHost(t, "http://"+h.addr("http")+"/", "a.test")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestRequestBodyLimit(t *testing.T) {
	s := siteConf("a.test", docroot(t, "x"), "http")
	s.Limits.MaxRequestBody = 100
	h := start(t, baseConfig(s))
	url := "http://" + h.addr("http") + "/"

	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(strings.Repeat("x", 1000)))
	req.Host = "a.test"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("a body over the limit: %d, want 413", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPost, url, strings.NewReader("small"))
	req.Host = "a.test"
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed { // within the limit; static files only allow GET and HEAD
		t.Errorf("a small POST: %d, want 405", resp.StatusCode)
	}
}

func TestAccessLogs(t *testing.T) {
	dir := t.TempDir()
	aLog, bLog := filepath.Join(dir, "a.access.log"), filepath.Join(dir, "b.access.log")
	sa := siteConf("a.test", docroot(t, "site-a"), "http")
	sa.Logs.Access = model.LogTarget{Path: aLog}
	sb := siteConf("b.test", docroot(t, "site-b"), "http")
	sb.Logs.Access = model.LogTarget{Path: bLog}
	sc := siteConf("c.test", docroot(t, "site-c"), "http")
	sc.Logs.Access = model.LogTarget{Off: true}
	sd := siteConf("d.test", docroot(t, "site-d"), "http") // default destination: stdout
	h := start(t, baseConfig(sa, sb, sc, sd))
	base := "http://" + h.addr("http")

	getHost(t, base+"/hello.txt?x=1", "a.test")
	getHost(t, base+"/missing", "a.test")
	getHost(t, base+"/", "b.test")
	getHost(t, base+"/", "c.test")
	getHost(t, base+"/", "d.test")
	getHost(t, base+"/", "bad host") // reaches no site: logged to the default access log

	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }
	a := read(aLog)
	if n := strings.Count(a, "\n"); n != 2 {
		t.Fatalf("site a log has %d lines, want 2:\n%s", n, a)
	}
	if !strings.Contains(a, `"GET /hello.txt?x=1 HTTP/1.1" 200 `) || !strings.Contains(a, `"GET /missing HTTP/1.1" 404 `) {
		t.Errorf("site a log:\n%s", a)
	}
	if !strings.HasPrefix(a, "127.0.0.1 - - [") {
		t.Errorf("client address missing:\n%s", a)
	}
	if b := read(bLog); strings.Count(b, "\n") != 1 || strings.Contains(b, "/hello.txt") {
		t.Errorf("site b log must only hold site b's request:\n%s", b)
	}
	out := h.stdout.String()
	if strings.Count(out, "\n") != 2 { // site d, plus the bad-host request
		t.Errorf("stdout should hold d.test's request and the unrouted one:\n%s", out)
	}
	if !strings.Contains(out, `" 400 `) {
		t.Errorf("the bad-Host request must be logged with its 400:\n%s", out)
	}
}

func TestAccessLogRecordsBytesOfFileResponses(t *testing.T) {
	root := docroot(t, "x")
	must(t, os.WriteFile(filepath.Join(root, "big.bin"), []byte(strings.Repeat("z", 1<<20)), 0o644))
	logPath := filepath.Join(t.TempDir(), "access.log")
	s := siteConf("a.test", root, "http")
	s.Logs.Access = model.LogTarget{Path: logPath}
	h := start(t, baseConfig(s))
	getHost(t, "http://"+h.addr("http")+"/big.bin", "a.test")
	b, _ := os.ReadFile(logPath)
	if !strings.Contains(string(b), `" 200 1048576 `) {
		t.Errorf("a 1 MiB file must be logged as 1048576 bytes (sendfile must not hide them):\n%s", b)
	}
}

func TestAccessLogIsInjectionSafe(t *testing.T) {
	h := start(t, baseConfig(siteConf("a.test", docroot(t, "x"), "http")))
	conn, err := net.Dial("tcp", h.addr("http"))
	must(t, err)
	defer conn.Close()
	// Go's server already rejects raw control characters and quotes in the
	// request line and headers (those requests never reach the handler), so use
	// what does get through: encoded CR/LF in the path, and quotes and
	// backslashes in headers.
	fmt.Fprint(conn, "GET /x%0d%0a9.9.9.9%20-%20-%20%22forged%22 HTTP/1.1\r\n"+
		"Host: a.test\r\n"+
		"User-Agent: evil\" 200 0 \"-\" \"fake\\\r\n"+
		"Referer: http://x.test/\" \"\r\n"+
		"Connection: close\r\n\r\n")
	io.Copy(io.Discard, conn)
	deadline := time.Now().Add(3 * time.Second)
	for h.stdout.String() == "" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	out := h.stdout.String()
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("one request must be one log line:\n%q", out)
	}
	if !strings.Contains(out, `evil\" 200 0 \"-\" \"fake\\"`) {
		t.Errorf("quotes and backslashes in the user agent must be escaped:\n%q", out)
	}
	if strings.Contains(out, "\r") {
		t.Errorf("CR in the log:\n%q", out)
	}
}

func TestConnectionLimit(t *testing.T) {
	cfg := baseConfig(siteConf("a.test", docroot(t, "x"), "http"))
	withLimits(cfg, func(l *model.Limits) { l.MaxConnections = 2 })
	h := start(t, cfg)

	open := func() net.Conn {
		c, err := net.Dial("tcp", h.addr("http"))
		must(t, err)
		return c
	}
	// Two idle connections fill the limit.
	c1, c2 := open(), open()
	defer c1.Close()
	defer c2.Close()
	time.Sleep(100 * time.Millisecond) // let the server accept both

	c3 := open()
	defer c3.Close()
	c3.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := c3.Read(make([]byte, 1)); err == nil {
		t.Error("a connection over the limit must be closed by the server")
	}
	if h.srv.RejectedConnections() < 1 {
		t.Errorf("RejectedConnections = %d", h.srv.RejectedConnections())
	}

	// Freeing a slot lets new connections in again.
	c1.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		c := open()
		fmt.Fprint(c, "GET / HTTP/1.1\r\nHost: a.test\r\nConnection: close\r\n\r\n")
		c.SetReadDeadline(time.Now().Add(time.Second))
		line, err := bufio.NewReader(c).ReadString('\n')
		c.Close()
		if err == nil && strings.Contains(line, "200") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the slot was never released after a connection closed")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestSlowHeadersAreCutOff(t *testing.T) {
	cfg := baseConfig(siteConf("a.test", docroot(t, "x"), "http"))
	withLimits(cfg, func(l *model.Limits) { l.HeaderTimeout = model.Duration(300 * time.Millisecond) })
	h := start(t, cfg)
	conn, err := net.Dial("tcp", h.addr("http"))
	must(t, err)
	defer conn.Close()
	fmt.Fprint(conn, "GET / HTTP/1.1\r\nHost: a.") // slowloris: never finishes the headers
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	io.Copy(io.Discard, conn)
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("the server held a half-open request for %v", d)
	}
}

func TestStartFailsCleanlyWhenAPortIsTaken(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	defer taken.Close()

	cfg := baseConfig(siteConf("a.test", docroot(t, "x"), "one", "two"))
	cfg.Listeners = []model.Listener{
		{Name: "one", Address: "127.0.0.1:0"},
		{Name: "two", Address: taken.Addr().String()},
	}
	srv, err := New(cfg, Options{Logger: nil, Stdout: io.Discard, Stderr: io.Discard})
	must(t, err)
	err = srv.Start()
	if err == nil || !strings.Contains(err.Error(), `"two"`) {
		t.Fatalf("Start error = %v, want it to name listener two", err)
	}
	if len(srv.Addrs()) != 0 {
		t.Errorf("listeners left bound after a failed start: %v", srv.Addrs())
	}
	srv.Shutdown(context.Background())
}

func TestStartTwiceIsAnError(t *testing.T) {
	h := start(t, baseConfig(siteConf("a.test", docroot(t, "x"), "http")))
	if err := h.srv.Start(); err == nil {
		t.Error("starting twice must fail")
	}
}

func TestBadLogPathFailsAtStartup(t *testing.T) {
	s := siteConf("a.test", docroot(t, "x"), "http")
	s.Logs.Access = model.LogTarget{Path: filepath.Join(t.TempDir(), "no-such-dir", "a.log")}
	_, err := New(baseConfig(s), Options{})
	if err == nil || !strings.Contains(err.Error(), "a.test") || !strings.Contains(err.Error(), "access log") {
		t.Errorf("error = %v", err)
	}
}

func TestMissingDocumentRootFailsAtStartup(t *testing.T) {
	_, err := New(baseConfig(siteConf("a.test", filepath.Join(t.TempDir(), "gone"), "http")), Options{})
	if err == nil || !strings.Contains(err.Error(), "a.test") {
		t.Errorf("error = %v", err)
	}
}

func TestGracefulShutdown(t *testing.T) {
	h := start(t, baseConfig(siteConf("a.test", docroot(t, "x"), "http")))
	addr := h.addr("http")
	if resp, _ := getHost(t, "http://"+addr+"/", "a.test"); resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.srv.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c.Close()
		t.Error("the listener must be closed after Shutdown")
	}
	if err := h.srv.Shutdown(ctx); err != nil {
		t.Errorf("a second Shutdown must be harmless: %v", err)
	}
}

func TestShutdownWaitsForRunningRequests(t *testing.T) {
	root := docroot(t, "x")
	must(t, os.WriteFile(filepath.Join(root, "big.bin"), []byte(strings.Repeat("z", 8<<20)), 0o644))
	h := start(t, baseConfig(siteConf("a.test", root, "http")))

	conn, err := net.Dial("tcp", h.addr("http"))
	must(t, err)
	defer conn.Close()
	fmt.Fprint(conn, "GET /big.bin HTTP/1.1\r\nHost: a.test\r\nConnection: close\r\n\r\n")
	br := bufio.NewReader(conn)
	if line, err := br.ReadString('\n'); err != nil || !strings.Contains(line, "200") {
		t.Fatalf("status line = %q, err = %v", line, err)
	}

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		done <- h.srv.Shutdown(ctx)
	}()
	n, err := io.Copy(io.Discard, br) // the response must still arrive in full
	if err != nil || n < 8<<20 {
		t.Errorf("read %d bytes, err = %v; a running response must complete during shutdown", n, err)
	}
	if err := <-done; err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

// ---- TLS ----

func tlsConfig(t *testing.T, minVersion string, sites ...model.Site) *model.Config {
	cfg := baseConfig(sites...)
	cfg.Listeners = []model.Listener{{Name: "https", Address: "127.0.0.1:0", HTTPS: true}}
	cfg.TLS.MinVersion = minVersion
	return cfg
}

func manualSite(t *testing.T, name string, body string, cert certFiles, aliases ...string) model.Site {
	s := siteConf(name, docroot(t, body), "https")
	s.TLS = model.TLSManual
	s.Certificate, s.CertificateKey = cert.certPath, cert.keyPath
	s.Aliases = aliases
	return s
}

func TestHTTPSWithHTTP2(t *testing.T) {
	cert := validCert(t, "a.test")
	h := start(t, tlsConfig(t, "1.2", manualSite(t, "a.test", "secure-a", cert)))
	client := httpsClient(t, "a.test", cert)
	resp, err := client.Get("https://" + h.addr("https") + "/")
	must(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "secure-a" {
		t.Errorf("%d %q", resp.StatusCode, body)
	}
	if resp.ProtoMajor != 2 {
		t.Errorf("negotiated %s, want HTTP/2 over TLS", resp.Proto)
	}
	if resp.TLS == nil || resp.TLS.NegotiatedProtocol != "h2" {
		t.Errorf("ALPN = %+v", resp.TLS)
	}
}

func TestHTTPSFallsBackToHTTP11(t *testing.T) {
	cert := validCert(t, "a.test")
	h := start(t, tlsConfig(t, "1.2", manualSite(t, "a.test", "x", cert)))
	client := httpsClient(t, "a.test", cert)
	client.Transport.(*http.Transport).ForceAttemptHTTP2 = false
	client.Transport.(*http.Transport).TLSClientConfig.NextProtos = []string{"http/1.1"}
	resp, err := client.Get("https://" + h.addr("https") + "/")
	must(t, err)
	resp.Body.Close()
	if resp.ProtoMajor != 1 || resp.StatusCode != 200 {
		t.Errorf("%s %d", resp.Proto, resp.StatusCode)
	}
}

func TestSNISelectsTheCertificate(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	a := makeCert(t, dir, []string{"a.test"}, now.Add(-time.Hour), now.Add(48*time.Hour*30))
	b := makeCert(t, dir, []string{"b.test", "*.wild.test"}, now.Add(-time.Hour), now.Add(48*time.Hour*30))
	cfg := tlsConfig(t, "1.2", manualSite(t, "a.test", "site-a", a), manualSite(t, "b.test", "site-b", b, "*.wild.test"))
	h := start(t, cfg)

	serverCert := func(sni string) string {
		conn, err := tls.Dial("tcp", h.addr("https"), &tls.Config{ServerName: sni, InsecureSkipVerify: true})
		if err != nil {
			t.Fatalf("SNI %q: %v", sni, err)
		}
		defer conn.Close()
		return conn.ConnectionState().PeerCertificates[0].Subject.CommonName
	}
	for sni, want := range map[string]string{
		"a.test": "a.test", "A.TEST": "a.test",
		"b.test":       "b.test",
		"x.wild.test":  "b.test", // wildcard alias
		"unknown.test": "a.test", // default site's certificate
	} {
		if got := serverCert(sni); got != want {
			t.Errorf("SNI %q got the certificate of %q, want %q", sni, got, want)
		}
	}
	// A client with no SNI at all (for example an IP address) gets the default.
	conn, err := tls.Dial("tcp", h.addr("https"), &tls.Config{InsecureSkipVerify: true})
	must(t, err)
	if cn := conn.ConnectionState().PeerCertificates[0].Subject.CommonName; cn != "a.test" {
		t.Errorf("no SNI: certificate of %q", cn)
	}
	conn.Close()

	// And the content follows the name.
	_, body := httpsGet(t, h.addr("https"), "b.test", b)
	if body != "site-b" {
		t.Errorf("body = %q", body)
	}
}

func httpsGet(t *testing.T, addr, name string, trust certFiles) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "https://"+addr+"/", nil)
	req.Host = name // the Host header must match the name used for SNI
	resp, err := httpsClient(t, name, trust).Do(req)
	must(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestMisdirectedRequestIsRefused(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	a := makeCert(t, dir, []string{"a.test"}, now.Add(-time.Hour), now.Add(1000*time.Hour))
	b := makeCert(t, dir, []string{"b.test"}, now.Add(-time.Hour), now.Add(1000*time.Hour))
	h := start(t, tlsConfig(t, "1.2", manualSite(t, "a.test", "site-a", a), manualSite(t, "b.test", "site-b", b)))

	// Connect for a.test, then ask for b.test on the same connection. (With
	// HTTP/2 connection reuse, a browser could do this on its own.)
	client := httpsClient(t, "a.test", a)
	req, _ := http.NewRequest(http.MethodGet, "https://"+h.addr("https")+"/", nil)
	req.Host = "b.test"
	resp, err := client.Do(req)
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusMisdirectedRequest || strings.Contains(string(body), "site-b") {
		t.Errorf("%d %q, want 421 and no content from site b", resp.StatusCode, body)
	}
	// Matching names are fine.
	req.Host = "a.test"
	resp, err = client.Do(req)
	must(t, err)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("matching Host: %d", resp.StatusCode)
	}
}

func TestMinimumTLSVersion(t *testing.T) {
	cert := validCert(t, "a.test")
	h := start(t, tlsConfig(t, "1.3", manualSite(t, "a.test", "x", cert)))
	try := func(max uint16) error {
		conn, err := tls.Dial("tcp", h.addr("https"), &tls.Config{ServerName: "a.test", InsecureSkipVerify: true, MaxVersion: max})
		if err == nil {
			conn.Close()
		}
		return err
	}
	if err := try(tls.VersionTLS12); err == nil {
		t.Error("a TLS 1.2 client must be refused when min_version is 1.3")
	}
	if err := try(tls.VersionTLS13); err != nil {
		t.Errorf("a TLS 1.3 client must be accepted: %v", err)
	}
}

func TestHTTPAndHTTPSTogether(t *testing.T) {
	cert := validCert(t, "a.test")
	s := siteConf("a.test", docroot(t, "both"), "http", "https")
	s.TLS = model.TLSManual
	s.Certificate, s.CertificateKey = cert.certPath, cert.keyPath
	cfg := baseConfig(s)
	cfg.Listeners = []model.Listener{{Name: "http", Address: "127.0.0.1:0"}, {Name: "https", Address: "127.0.0.1:0", HTTPS: true}}
	h := start(t, cfg)

	if _, body := getHost(t, "http://"+h.addr("http")+"/", "a.test"); body != "both" {
		t.Errorf("http: %q", body)
	}
	if _, body := httpsGet(t, h.addr("https"), "a.test", cert); body != "both" {
		t.Errorf("https: %q", body)
	}
}

func TestCertificateWarnings(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name  string
		cert  certFiles
		warns string
	}{
		{"expired", makeCert(t, t.TempDir(), []string{"a.test"}, now.Add(-48*time.Hour), now.Add(-time.Hour)), "certificate has expired"},
		{"expiring soon", makeCert(t, t.TempDir(), []string{"a.test"}, now.Add(-time.Hour), now.Add(3*24*time.Hour)), "expires soon"},
		{"wrong name", makeCert(t, t.TempDir(), []string{"other.test"}, now.Add(-time.Hour), now.Add(90*24*time.Hour)), "does not cover"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := start(t, tlsConfig(t, "1.2", manualSite(t, "a.test", "x", c.cert)))
			if logs := h.logs.String(); !strings.Contains(logs, c.warns) {
				t.Errorf("server log lacks %q:\n%s", c.warns, logs)
			}
		})
	}
	t.Run("healthy certificate gives no warning", func(t *testing.T) {
		h := start(t, tlsConfig(t, "1.2", manualSite(t, "a.test", "x", validCert(t, "a.test"))))
		if logs := h.logs.String(); strings.Contains(logs, "level=WARN") {
			t.Errorf("unexpected warning:\n%s", logs)
		}
	})
	t.Run("a wildcard alias is checked against a wildcard certificate", func(t *testing.T) {
		cert := validCert(t, "a.test", "*.a.test")
		h := start(t, tlsConfig(t, "1.2", manualSite(t, "a.test", "x", cert, "*.a.test")))
		if logs := h.logs.String(); strings.Contains(logs, "does not cover") {
			t.Errorf("unexpected warning:\n%s", logs)
		}
	})
}

func TestUnreadableCertificateFailsAtStartupWithoutLeakingKeyMaterial(t *testing.T) {
	cert := validCert(t, "a.test")
	keyBytes, _ := os.ReadFile(cert.keyPath)
	must(t, os.WriteFile(cert.keyPath, []byte("not a key"), 0o600))
	s := manualSite(t, "a.test", "x", cert)
	_, err := New(tlsConfig(t, "1.2", s), Options{})
	if err == nil || !strings.Contains(err.Error(), `"a.test"`) {
		t.Fatalf("error = %v, want it to name the site", err)
	}
	if strings.Contains(err.Error(), "not a key") || strings.Contains(err.Error(), string(keyBytes[:40])) {
		t.Errorf("error leaks key material: %v", err)
	}

	s.Certificate = filepath.Join(t.TempDir(), "missing.pem")
	if _, err := New(tlsConfig(t, "1.2", s), Options{}); err == nil {
		t.Error("a missing certificate file must fail at startup")
	}
}

func TestHandshakeWithUnknownSiteAndNoCertificateFails(t *testing.T) {
	// A listener with no sites has no certificate to offer.
	cfg := tlsConfig(t, "1.2")
	h := start(t, cfg)
	conn, err := tls.Dial("tcp", h.addr("https"), &tls.Config{ServerName: "a.test", InsecureSkipVerify: true})
	if err == nil {
		conn.Close()
		t.Error("the handshake must fail when there is no certificate to offer")
	}
}

func TestFailedStartReleasesFiles(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	defer taken.Close()
	root := docroot(t, "x")
	logPath := filepath.Join(t.TempDir(), "a.log")
	s := siteConf("a.test", root, "http")
	s.Logs.Access = model.LogTarget{Path: logPath}
	cfg := baseConfig(s)
	cfg.Listeners[0].Address = taken.Addr().String()

	srv, err := New(cfg, Options{Stdout: io.Discard, Stderr: io.Discard})
	must(t, err)
	if err := srv.Start(); err == nil {
		t.Fatal("Start must fail: the port is taken")
	}
	// On Windows an open handle makes these fail; elsewhere this checks the
	// bookkeeping.
	if err := os.RemoveAll(root); err != nil {
		t.Errorf("the document root is still held open after a failed start: %v", err)
	}
	if err := os.Remove(logPath); err != nil {
		t.Errorf("the log file is still held open after a failed start: %v", err)
	}
	if len(srv.closers) != 0 {
		t.Errorf("%d resources not released", len(srv.closers))
	}
}
