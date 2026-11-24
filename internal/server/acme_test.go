package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/acme"
	"github.com/laikait/lotus/internal/config/model"
)

// These tests run a real ACME server (Pebble, Let's Encrypt's test CA) and its
// mock DNS. They need two programs and Pebble's test certificates:
//
//	LOTUS_PEBBLE_DIR      a directory with pebble and pebble-challtestsrv
//	LOTUS_PEBBLE_TESTDIR  Pebble's test/ directory (test/certs/...)
//
// Without them the tests are skipped.
//
//	go install github.com/letsencrypt/pebble/v2/cmd/pebble@v2.10.3
//	go install github.com/letsencrypt/pebble/v2/cmd/pebble-challtestsrv@v2.10.3

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func exe(name string) string {
	if os.PathSeparator == '\\' {
		return name + ".exe"
	}
	return name
}

type pebbleEnv struct {
	dirURL string
	root   string // PEM file: the root that signed Pebble's own TLS certificate
	mgmt   string
	http   int // where Pebble validates HTTP-01
	tls    int // where Pebble validates TLS-ALPN-01
}

func startPebble(t *testing.T) *pebbleEnv {
	t.Helper()
	bin, testDir := os.Getenv("LOTUS_PEBBLE_DIR"), os.Getenv("LOTUS_PEBBLE_TESTDIR")
	if bin == "" || testDir == "" {
		t.Skip("set LOTUS_PEBBLE_DIR and LOTUS_PEBBLE_TESTDIR to run the ACME tests against Pebble")
	}
	env := &pebbleEnv{http: freePort(t), tls: freePort(t)}
	acmePort, mgmtPort, dnsPort, chMgmt := freePort(t), freePort(t), freePort(t), freePort(t)
	env.dirURL = fmt.Sprintf("https://localhost:%d/dir", acmePort)
	env.mgmt = fmt.Sprintf("localhost:%d", mgmtPort)
	env.root = filepath.Join(testDir, "certs", "pebble.minica.pem")

	dir := t.TempDir()
	cfg := map[string]any{"pebble": map[string]any{
		"listenAddress":           fmt.Sprintf("127.0.0.1:%d", acmePort),
		"managementListenAddress": fmt.Sprintf("127.0.0.1:%d", mgmtPort),
		"certificate":             filepath.Join(testDir, "certs", "localhost", "cert.pem"),
		"privateKey":              filepath.Join(testDir, "certs", "localhost", "key.pem"),
		"httpPort":                env.http, "tlsPort": env.tls,
		"ocspResponderURL": "", "externalAccountBindingRequired": false,
		"retryAfter":   map[string]int{"authz": 1, "order": 1},
		"keyAlgorithm": "ecdsa",
		"profiles":     map[string]any{"default": map[string]any{"description": "d", "validityPeriod": 7776000}},
	}}
	b, _ := json.Marshal(cfg)
	cfgFile := filepath.Join(dir, "pebble.json")
	must(t, os.WriteFile(cfgFile, b, 0o600))

	run := func(name string, args ...string) {
		cmd := exec.Command(filepath.Join(bin, exe(name)), args...)
		cmd.Env = append(os.Environ(), "PEBBLE_VA_NOSLEEP=1", "PEBBLE_WFE_NONCEREJECT=0")
		var out syncBuffer
		cmd.Stdout, cmd.Stderr = &out, &out
		must(t, cmd.Start())
		t.Cleanup(func() {
			cmd.Process.Kill()
			cmd.Wait()
			if t.Failed() {
				t.Logf("%s output:\n%s", name, tail(out.String(), 3000))
			}
		})
	}
	// The mock DNS answers every name with 127.0.0.1, so Pebble validates
	// whatever name we ask for against this machine.
	run("pebble-challtestsrv", "-defaultIPv4", "127.0.0.1", "-defaultIPv6", "",
		"-dnsserver", fmt.Sprintf("127.0.0.1:%d", dnsPort), "-management", fmt.Sprintf("127.0.0.1:%d", chMgmt),
		"-http01", "", "-https01", "", "-tlsalpn01", "", "-doh", "")
	run("pebble", "-config", cfgFile, "-dnsserver", fmt.Sprintf("127.0.0.1:%d", dnsPort))

	waitTCP(t, fmt.Sprintf("127.0.0.1:%d", acmePort))
	return env
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func waitTCP(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("nothing listens on %s", addr)
}

// pebbleRoots fetches the root and intermediate that sign Pebble's certificates.
func (e *pebbleEnv) pebbleRoots(t *testing.T) (roots *x509.CertPool, inters []*x509.Certificate) {
	t.Helper()
	pem0, err := os.ReadFile(e.root)
	must(t, err)
	trust := x509.NewCertPool()
	trust.AppendCertsFromPEM(pem0)
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: trust}}}
	get := func(path string) []*x509.Certificate {
		resp, err := c.Get("https://" + e.mgmt + path)
		must(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var out []*x509.Certificate
		for {
			var blk *pem.Block
			blk, b = pem.Decode(b)
			if blk == nil {
				break
			}
			cert, err := x509.ParseCertificate(blk.Bytes)
			must(t, err)
			out = append(out, cert)
		}
		if len(out) == 0 {
			t.Fatalf("%s: no certificate", path)
		}
		return out
	}
	roots = x509.NewCertPool()
	for _, c := range get("/roots/0") {
		roots.AddCert(c)
	}
	return roots, get("/intermediates/0")
}

// lotusACME starts a LOTUS server with two listeners on the ports Pebble
// validates, and the given sites using automatic certificates.
func lotusACME(t *testing.T, env *pebbleEnv, storage string, challenges []string, names ...string) (*Server, *model.Config) {
	t.Helper()
	root := docroot(t, "acme-site")
	var sites []model.Site
	for _, n := range names {
		s := siteConf(n, root, "http", "https")
		s.TLS = model.TLSAutomatic
		sites = append(sites, s)
	}
	cfg := baseConfig(sites...)
	cfg.Listeners = []model.Listener{
		{Name: "http", Address: fmt.Sprintf("127.0.0.1:%d", env.http)},
		{Name: "https", Address: fmt.Sprintf("127.0.0.1:%d", env.tls), HTTPS: true},
	}
	cfg.TLS.ACME = &model.ACME{
		Email: "admin@example.test", CA: env.dirURL, CARoot: env.root, Storage: storage,
		HTTPPort: env.http, TLSALPNPort: env.tls, Challenges: challenges,
	}
	h := start(t, cfg)
	return h.srv, cfg
}

// handshake connects to the https port with the given server name.
func handshake(t *testing.T, env *pebbleEnv, name string) (*tls.ConnectionState, error) {
	t.Helper()
	d := net.Dialer{Timeout: 3 * time.Second}
	raw, err := d.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", env.tls))
	if err != nil {
		return nil, err
	}
	defer raw.Close()
	c := tls.Client(raw, &tls.Config{ServerName: name, InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}}) //nolint:gosec // the chain is checked by the test
	c.SetDeadline(time.Now().Add(3 * time.Second))
	if err := c.Handshake(); err != nil {
		return nil, err
	}
	st := c.ConnectionState()
	return &st, nil
}

func waitCert(t *testing.T, env *pebbleEnv, name string) *x509.Certificate {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		st, err := handshake(t, env, name)
		if err == nil && len(st.PeerCertificates) > 0 && strings.Contains(st.PeerCertificates[0].Issuer.String(), "Pebble") {
			return st.PeerCertificates[0]
		}
		last = err
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("no certificate for %s after 40s (last handshake error: %v)", name, last)
	return nil
}

func TestAutomaticCertificatesWithPebble(t *testing.T) {
	for _, challenge := range []string{"http-01", "tls-alpn-01"} {
		t.Run(challenge, func(t *testing.T) {
			env := startPebble(t)
			storage := filepath.Join(t.TempDir(), "certs")
			srv, cfg := lotusACME(t, env, storage, []string{challenge}, "lotus-acme.test")

			leaf := waitCert(t, env, "lotus-acme.test")
			if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "lotus-acme.test" {
				t.Errorf("certificate names: %v", leaf.DNSNames)
			}

			// The chain verifies against Pebble's root, for the right name.
			roots, inters := env.pebbleRoots(t)
			st, err := handshake(t, env, "lotus-acme.test")
			must(t, err)
			ip := x509.NewCertPool()
			for _, c := range append(append([]*x509.Certificate(nil), st.PeerCertificates[1:]...), inters...) {
				ip.AddCert(c)
			}
			if _, err := st.PeerCertificates[0].Verify(x509.VerifyOptions{DNSName: "lotus-acme.test", Roots: roots, Intermediates: ip}); err != nil {
				t.Errorf("the certificate does not verify: %v", err)
			}

			// And a real HTTPS request is served by the site.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client := &http.Client{Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", env.tls))
				},
				TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "lotus-acme.test", VerifyConnection: func(cs tls.ConnectionState) error {
					_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{DNSName: "lotus-acme.test", Roots: roots, Intermediates: ip})
					return err
				}, InsecureSkipVerify: true}, //nolint:gosec // VerifyConnection does the verification with the intermediates
			}}
			req, _ := http.NewRequestWithContext(ctx, "GET", "https://lotus-acme.test/hello.txt", nil)
			resp, err := client.Do(req)
			must(t, err)
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 || string(body) != "hello from acme-site" {
				t.Errorf("%d %q", resp.StatusCode, body)
			}

			// A name that is not a site is never given a certificate, and asking
			// for one costs the CA nothing.
			if _, err := handshake(t, env, "not-a-site.test"); err == nil {
				t.Error("a handshake for an unknown name succeeded")
			}

			// The certificate and its key are on disk, readable by the server only.
			if runtimeUnix() {
				if fi, err := os.Stat(storage); err != nil || fi.Mode().Perm()&0o077 != 0 {
					t.Errorf("storage permissions: %v %v", fi, err)
				}
			}
			srv.Shutdown(context.Background())

			// A new server with the same storage serves the same certificate at once.
			_, _ = lotusACME(t, env, storage, []string{challenge}, "lotus-acme.test")
			st2, err := handshake(t, env, "lotus-acme.test")
			must(t, err)
			if st2.PeerCertificates[0].SerialNumber.Cmp(leaf.SerialNumber) != 0 {
				t.Error("a restart ordered a new certificate instead of using the stored one")
			}
			_ = cfg

			// Stored certificates are available the moment Manage returns, not some
			// time later, and without asking the CA. Many fresh managers, so a
			// loader that only ran in the background would be caught.
			for i := 0; i < 25; i++ {
				m, err := acme.New(acme.Config{Storage: storage, CA: env.dirURL, CARoot: env.root, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
				must(t, err)
				m.Manage([]string{"lotus-acme.test"})
				c1, c2 := net.Pipe()
				cert, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: "lotus-acme.test", Conn: c1})
				c1.Close()
				c2.Close()
				m.Close()
				if err != nil || cert == nil {
					t.Fatalf("attempt %d: a stored certificate was not available right after Manage: %v", i, err)
				}
			}
		})
	}
}

func runtimeUnix() bool { return os.PathSeparator == '/' }

func TestAutomaticCertificatesForSitesAddedByReload(t *testing.T) {
	env := startPebble(t)
	storage := filepath.Join(t.TempDir(), "certs")
	srv, cfg := lotusACME(t, env, storage, nil, "first.lotus-acme.test")
	waitCert(t, env, "first.lotus-acme.test")

	cfg2 := *cfg
	root := docroot(t, "second")
	s2 := siteConf("second.lotus-acme.test", root, "http", "https")
	s2.TLS = model.TLSAutomatic
	cfg2.Sites = append(append([]model.Site(nil), cfg.Sites...), s2)
	must(t, srv.Reload(&cfg2))
	waitCert(t, env, "second.lotus-acme.test")

	// Changing how certificates are obtained needs a restart.
	cfg3 := cfg2
	a := *cfg2.TLS.ACME
	a.Email = "someone-else@example.test"
	cfg3.TLS.ACME = &a
	var rr *RestartRequiredError
	if err := srv.Reload(&cfg3); !errors.As(err, &rr) {
		t.Errorf("changing the ACME account in a reload: %v", err)
	}
}
