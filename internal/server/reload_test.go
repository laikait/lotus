package server

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/config/model"
)

func bodyOf(t *testing.T, base, host string) string {
	t.Helper()
	resp, body := getHost(t, base+"/", host)
	if resp.StatusCode != 200 {
		t.Fatalf("GET / (Host %s) = %d %q", host, resp.StatusCode, body)
	}
	return body
}

func TestReloadSwitchesTheConfiguration(t *testing.T) {
	rootA, rootB, rootC := docroot(t, "v1"), docroot(t, "v2"), docroot(t, "other")
	h := start(t, baseConfig(siteConf("a.test", rootA, "http")))
	base := "http://" + h.addr("http")
	if got := bodyOf(t, base, "a.test"); got != "v1" {
		t.Fatalf("before reload: %q", got)
	}

	// A different root, a new site and a new alias.
	a := siteConf("a.test", rootB, "http")
	a.Aliases = []string{"www.a.test"}
	cfg2 := baseConfig(a, siteConf("b.test", rootC, "http"))
	if err := h.srv.Reload(cfg2); err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]string{"a.test": "v2", "www.a.test": "v2", "b.test": "other"} {
		if got := bodyOf(t, base, host); got != want {
			t.Errorf("Host %s after reload: %q, want %q", host, got, want)
		}
	}

	// And back: a site disappears.
	if err := h.srv.Reload(baseConfig(siteConf("a.test", rootA, "http"))); err != nil {
		t.Fatal(err)
	}
	if got := bodyOf(t, base, "b.test"); got != "v1" { // b.test is gone: the default site answers
		t.Errorf("a removed site must not be served any more: %q", got)
	}
	if !strings.Contains(h.logs.String(), "configuration reloaded") {
		t.Errorf("a reload should be logged: %s", h.logs.String())
	}
}

func TestFailedReloadKeepsTheRunningConfiguration(t *testing.T) {
	root := docroot(t, "stable")
	h := start(t, baseConfig(siteConf("a.test", root, "http")))
	base := "http://" + h.addr("http")

	missing := filepath.Join(t.TempDir(), "no", "such", "dir")
	badLog := siteConf("a.test", docroot(t, "x"), "http")
	badLog.Logs.Access = model.LogTarget{Path: filepath.Join(missing, "a.log")}

	for name, cfg := range map[string]*model.Config{
		"missing document root": baseConfig(siteConf("a.test", missing, "http")),
		"unwritable log":        baseConfig(badLog),
		"unreadable certificate": func() *model.Config {
			s := siteConf("a.test", root, "http")
			s.TLS, s.Certificate, s.CertificateKey = model.TLSManual, filepath.Join(missing, "c.pem"), filepath.Join(missing, "c.key")
			return baseConfig(s)
		}(),
	} {
		err := h.srv.Reload(cfg)
		if err == nil || !strings.Contains(err.Error(), "the running configuration was kept") {
			t.Errorf("%s: err = %v", name, err)
		}
		if got := bodyOf(t, base, "a.test"); got != "stable" {
			t.Errorf("%s: the old configuration must keep serving, got %q", name, got)
		}
	}
}

func TestReloadRefusesWhatNeedsARestart(t *testing.T) {
	root := docroot(t, "x")
	h := start(t, baseConfig(siteConf("a.test", root, "http")))
	base := "http://" + h.addr("http")

	tests := map[string]func(*model.Config){
		"listener address": func(c *model.Config) { c.Listeners[0].Address = "127.0.0.1:1" },
		"listener https":   func(c *model.Config) { c.Listeners[0].HTTPS = true },
		"new listener": func(c *model.Config) {
			c.Listeners = append(c.Listeners, model.Listener{Name: "x", Address: "127.0.0.1:0"})
		},
		"connection limit": func(c *model.Config) { withLimits(c, func(l *model.Limits) { l.MaxConnections = 5 }) },
		"timeout": func(c *model.Config) {
			withLimits(c, func(l *model.Limits) { l.IdleTimeout = model.Duration(time.Second) })
		},
		"tls min version": func(c *model.Config) { c.TLS.MinVersion = "1.3" },
	}
	for name, mutate := range tests {
		cfg := baseConfig(siteConf("a.test", root, "http"))
		mutate(cfg)
		err := h.srv.Reload(cfg)
		var rr *RestartRequiredError
		if !errors.As(err, &rr) {
			t.Errorf("%s: err = %v, want a RestartRequiredError", name, err)
		}
		if got := bodyOf(t, base, "a.test"); got != "x" {
			t.Errorf("%s: the running configuration changed: %q", name, got)
		}
	}

	// A per-site max_request_body is not one of them.
	cfg := baseConfig(siteConf("a.test", root, "http"))
	cfg.Sites[0].Limits.MaxRequestBody = 1 << 10
	if err := h.srv.Reload(cfg); err != nil {
		t.Errorf("a per-site body limit can be reloaded: %v", err)
	}
}

func TestReloadRefusesUnsupportedFeatures(t *testing.T) {
	root := docroot(t, "x")
	h := start(t, baseConfig(siteConf("a.test", root, "http")))
	cfg := baseConfig(siteConf("a.test", root, "http"))
	cfg.Control.ViewerGroup = "lotus-viewers" // unsupported
	var ue *UnsupportedError
	if err := h.srv.Reload(cfg); !errors.As(err, &ue) {
		t.Errorf("err = %v", err)
	}
}

func TestReloadAppliesRoutesAndTrustedProxies(t *testing.T) {
	root := docroot(t, "x")
	h := start(t, baseConfig(siteConf("a.test", root, "http")))
	base := "http://" + h.addr("http")

	s := siteConf("a.test", root, "http")
	s.Limits.MaxRequestBody = model.DefaultMaxRequestBody
	s.Routes = []model.Route{{
		Kind: model.RoutePrefix, Path: "/", Root: root, Index: s.Index, MaxRequestBody: s.Limits.MaxRequestBody,
		Actions: ret(200, "ip={remote_ip}"),
	}}
	cfg := baseConfig(s)
	cfg.TrustedProxies = []netipPrefix{mustPrefix("127.0.0.0/8")}
	if err := h.srv.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	_, body := do(t, "GET", base+"/", map[string]string{"X-Forwarded-For": "203.0.113.9"}, "")
	if strings.TrimSpace(body) != "ip=203.0.113.9" {
		t.Errorf("routes and trusted_proxies must apply after a reload: %q", body)
	}
}

func TestReloadChangesTheCertificate(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	c1 := makeCert(t, dir, []string{"a.test"}, now.Add(-time.Hour), now.Add(48*time.Hour*30))
	dir2 := t.TempDir()
	c2 := makeCert(t, dir2, []string{"a.test"}, now.Add(-time.Hour), now.Add(48*time.Hour*30))
	if c1.cert.SerialNumber.Cmp(c2.cert.SerialNumber) == 0 {
		t.Skip("identical serials")
	}
	root := docroot(t, "secure")
	mk := func(c certFiles) *model.Config {
		s := siteConf("a.test", root, "https")
		s.TLS, s.Certificate, s.CertificateKey = model.TLSManual, c.certPath, c.keyPath
		return tlsConfig(t, "1.2", s)
	}
	h := start(t, mk(c1))
	serial := func() string {
		conn, err := tls.Dial("tcp", h.addr("https"), &tls.Config{ServerName: "a.test", InsecureSkipVerify: true})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		return conn.ConnectionState().PeerCertificates[0].SerialNumber.String()
	}
	if got := serial(); got != c1.cert.SerialNumber.String() {
		t.Fatalf("before reload: serial %s", got)
	}
	if err := h.srv.Reload(mk(c2)); err != nil {
		t.Fatal(err)
	}
	if got := serial(); got != c2.cert.SerialNumber.String() {
		t.Errorf("after reload new handshakes must use the new certificate: serial %s, want %s", got, c2.cert.SerialNumber)
	}
}

func TestReloadReopensLogFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("an open log file can't be renamed on Windows")
	}
	root := docroot(t, "x")
	logPath := filepath.Join(t.TempDir(), "access.log")
	mk := func() *model.Config {
		s := siteConf("a.test", root, "http")
		s.Logs.Access = model.LogTarget{Path: logPath}
		return baseConfig(s)
	}
	h := start(t, mk())
	base := "http://" + h.addr("http")

	logged := func(path, want string) bool {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if b, err := os.ReadFile(path); err == nil && strings.Contains(string(b), want) {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return false
	}
	getHost(t, base+"/before", "a.test")
	if !logged(logPath, "/before") {
		t.Fatal("the request was not logged")
	}

	// Log rotation: move the file away, then reload.
	rotated := logPath + ".1"
	if err := os.Rename(logPath, rotated); err != nil {
		t.Fatal(err)
	}
	getHost(t, base+"/to-old-file", "a.test") // still goes to the moved file
	if !logged(rotated, "/to-old-file") {
		t.Error("before the reload the old handle keeps writing to the moved file")
	}
	if err := h.srv.Reload(mk()); err != nil {
		t.Fatal(err)
	}
	getHost(t, base+"/after", "a.test")
	if !logged(logPath, "/after") {
		t.Error("after the reload a new file must be created at the configured path")
	}
	if b, _ := os.ReadFile(rotated); strings.Contains(string(b), "/after") {
		t.Error("the rotated file must not receive new entries")
	}
}

func TestOldResourcesAreReleasedAfterReload(t *testing.T) {
	rootA, rootB := docroot(t, "a"), docroot(t, "b")
	h := start(t, baseConfig(siteConf("a.test", rootA, "http")))
	if err := h.srv.Reload(baseConfig(siteConf("a.test", rootB, "http"))); err != nil {
		t.Fatal(err)
	}
	// On Windows an open handle would make this fail.
	if err := os.RemoveAll(rootA); err != nil {
		t.Errorf("the old document root is still open after a reload: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(rootB); err != nil {
		t.Errorf("shutdown must close the current configuration's files: %v", err)
	}
}

func TestInFlightRequestsKeepTheirGeneration(t *testing.T) {
	root := docroot(t, "x")
	h := start(t, baseConfig(siteConf("a.test", root, "http")))

	old := h.srv.acquire() // a request that is still running
	if old == nil {
		t.Fatal("acquire")
	}
	if err := h.srv.Reload(baseConfig(siteConf("a.test", docroot(t, "y"), "http"))); err != nil {
		t.Fatal(err)
	}
	if old.closed {
		t.Fatal("a generation with a running request must stay open")
	}
	old.release()
	if !old.closed {
		t.Error("the old generation must close when its last request ends")
	}
	if h.srv.gen.Load() == old {
		t.Error("the new generation must be current")
	}
}

func TestGenerationLifecycle(t *testing.T) {
	var closed int
	g := &generation{closers: []io.Closer{closerFunc(func() { closed++ })}}
	for i := 0; i < 2; i++ { // two requests are running
		if !g.acquire() {
			t.Fatal("acquire")
		}
	}
	g.retire()
	if closed != 0 {
		t.Error("closed while two requests run")
	}
	g.release()
	if closed != 0 {
		t.Error("closed while one request runs")
	}
	g.release()
	if closed != 1 {
		t.Errorf("closed %d times after the last release", closed)
	}
	if g.acquire() {
		t.Error("a closed generation must refuse new requests")
	}
	g.closeNow()
	g.retire()
	if closed != 1 {
		t.Errorf("closing must happen once, got %d", closed)
	}
}

type closerFunc func()

func (f closerFunc) Close() error { f(); return nil }

// Requests that arrive while configurations are being swapped must always get
// a complete answer from one configuration or the other.
func TestRequestsDuringReloadsAreAlwaysAnswered(t *testing.T) {
	rootA, rootB := docroot(t, "v1"), docroot(t, "v2")
	h := start(t, baseConfig(siteConf("a.test", rootA, "http")))
	base := "http://" + h.addr("http")

	var stop atomic.Bool
	var bad atomic.Int64
	var total atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				resp, body, err := tryGet(base+"/", "a.test")
				total.Add(1)
				if err != nil || resp != 200 || (body != "v1" && body != "v2") {
					bad.Add(1)
				}
			}
		}()
	}
	for i := 0; i < 80; i++ {
		time.Sleep(4 * time.Millisecond) // let requests run between swaps
		root := rootA
		if i%2 == 0 {
			root = rootB
		}
		if err := h.srv.Reload(baseConfig(siteConf("a.test", root, "http"))); err != nil {
			t.Fatalf("reload %d: %v", i, err)
		}
	}
	stop.Store(true)
	wg.Wait()
	http.DefaultTransport.(*http.Transport).CloseIdleConnections()
	if bad.Load() != 0 {
		t.Errorf("%d of %d requests failed or got a broken answer during reloads", bad.Load(), total.Load())
	}
	if total.Load() < 100 {
		t.Errorf("only %d requests ran; the test proves little", total.Load())
	}
}

// tryGet is getHost for goroutines: it reports errors instead of failing the test.
func tryGet(url, host string) (status int, body string, err error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
	}
	req.Host = host
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), err
}
