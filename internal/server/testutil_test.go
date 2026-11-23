package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/config/model"
)

// syncBuffer is a bytes.Buffer that is safe for concurrent use.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// docroot creates a document root with a marker file whose content is body.
func docroot(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	must(t, os.WriteFile(filepath.Join(root, "index.html"), []byte(body), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello from "+body), 0o644))
	must(t, os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=1"), 0o644))
	return root
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// siteConf builds a site with safe defaults, attached to the given listeners.
func siteConf(name, root string, listeners ...string) model.Site {
	return model.Site{
		Name:      name,
		Listeners: listeners,
		TLS:       model.TLSOff,
		Root:      root,
		Index:     []string{"index.html"},
		Limits:    model.DefaultLimits(),
		Symlinks:  model.SymlinksDeny,
		Dotfiles:  model.DotfilesDeny,
	}
}

func baseConfig(sites ...model.Site) *model.Config {
	return &model.Config{
		Version:   1,
		Limits:    model.DefaultLimits(),
		TLS:       model.TLS{MinVersion: "1.2"},
		Listeners: []model.Listener{{Name: "http", Address: "127.0.0.1:0"}},
		Sites:     sites,
	}
}

type harness struct {
	srv    *Server
	stdout *syncBuffer
	stderr *syncBuffer
	logs   *syncBuffer // server events
}

// start builds and starts a server, and stops it when the test ends.
func start(t *testing.T, cfg *model.Config, mutate ...func(*Options)) *harness {
	t.Helper()
	h := &harness{stdout: &syncBuffer{}, stderr: &syncBuffer{}, logs: &syncBuffer{}}
	opts := Options{
		Logger: slog.New(slog.NewTextHandler(h.logs, nil)),
		Stdout: h.stdout,
		Stderr: h.stderr,
	}
	for _, m := range mutate {
		m(&opts)
	}
	srv, err := New(cfg, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	must(t, srv.Start())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	})
	h.srv = srv
	return h
}

func (h *harness) addr(listener string) string { return h.srv.Addrs()[listener].String() }

// getHost requests path with the given Host header.
func getHost(t *testing.T, url, host string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Host = host
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET %s (Host %s): %v", url, host, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// ---- certificates ----

type certFiles struct {
	certPath, keyPath string
	cert              *x509.Certificate
}

// makeCert writes a self-signed certificate for names, valid for the given window.
func makeCert(t *testing.T, dir string, names []string, notBefore, notAfter time.Time) certFiles {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(t, err)
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true, BasicConstraintsValid: true, // lets a test client trust it directly
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	must(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	must(t, err)

	cf := certFiles{
		certPath: filepath.Join(dir, names[0]+".pem"),
		keyPath:  filepath.Join(dir, names[0]+".key"),
	}
	must(t, os.WriteFile(cf.certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644))
	must(t, os.WriteFile(cf.keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600))
	cf.cert, err = x509.ParseCertificate(der)
	must(t, err)
	return cf
}

func validCert(t *testing.T, names ...string) certFiles {
	now := time.Now()
	return makeCert(t, t.TempDir(), names, now.Add(-time.Hour), now.Add(90*24*time.Hour))
}

func httpsClient(t *testing.T, serverName string, trust ...certFiles) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	for _, c := range trust {
		pool.AddCert(c.cert)
	}
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{RootCAs: pool, ServerName: serverName},
			ForceAttemptHTTP2: true,
		},
	}
}

type netipPrefix = netip.Prefix

func mustPrefix(s string) netip.Prefix { return netip.MustParsePrefix(s) }

func asUnsupported(err error, target **UnsupportedError) bool { return errors.As(err, target) }

// withLimits changes the server limits and gives every site the same values,
// as the compiler does when a site doesn't set its own.
func withLimits(cfg *model.Config, change func(*model.Limits)) {
	change(&cfg.Limits)
	for i := range cfg.Sites {
		body := cfg.Sites[i].Limits.MaxRequestBody
		cfg.Sites[i].Limits = cfg.Limits
		cfg.Sites[i].Limits.MaxRequestBody = body
	}
}

func mustRe(p string) *regexp.Regexp { return regexp.MustCompile(p) }
