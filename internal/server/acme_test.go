package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/miekg/dns"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	dns    string // the mock DNS server Pebble asks (and LOTUS asks for zones)
	chMgmt string // its management API: where TXT records are set
	http   int    // where Pebble validates HTTP-01
	tls    int    // where Pebble validates TLS-ALPN-01
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
	env.dns = fmt.Sprintf("127.0.0.1:%d", dnsPort)
	env.chMgmt = fmt.Sprintf("127.0.0.1:%d", chMgmt)
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
	return lotusACMEWith(t, env, storage, challenges, nil, names...)
}

// lotusACMEWith is lotusACME with a dns block. A name that starts with "*." is
// an alias of the site named after the rest.
func lotusACMEWith(t *testing.T, env *pebbleEnv, storage string, challenges []string, dns *model.DNSChallenge, names ...string) (*Server, *model.Config) {
	t.Helper()
	root := docroot(t, "acme-site")
	var sites []model.Site
	for _, n := range names {
		if strings.HasPrefix(n, "*.") {
			sites[len(sites)-1].Aliases = append(sites[len(sites)-1].Aliases, n)
			continue
		}
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
		HTTPPort: env.http, TLSALPNPort: env.tls, Challenges: challenges, DNS: dns,
	}
	h := start(t, cfg)
	t.Cleanup(func() {
		if t.Failed() {
			var keep []string
			for _, l := range strings.Split(h.logs.String(), "\n") {
				if !strings.Contains(l, "TLS handshake error") { // one per poll of waitCert
					keep = append(keep, l)
				}
			}
			t.Logf("LOTUS log:\n%s", tail(strings.Join(keep, "\n"), 5000))
		}
	})
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

			// The certificate is listed with its issuer and expiry (lotus certs, metrics).
			var listed bool
			for _, c := range srv.Certificates() {
				if c.Name == "lotus-acme.test" && c.Source == "acme" && c.Ready && strings.Contains(c.Issuer, "Pebble") &&
					len(c.Domains) == 1 && c.Domains[0] == "lotus-acme.test" && c.NotAfter.Equal(leaf.NotAfter) {
					listed = true
				}
			}
			if !listed {
				t.Errorf("the obtained certificate is not listed: %+v", srv.Certificates())
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

// dnsHelper is this test binary acting as the program of the exec DNS provider:
// it sets or clears the TXT record in the mock DNS server's management API.
func dnsHelper() {
	action, name, value := os.Args[1], os.Getenv("LOTUS_ACME_NAME"), os.Getenv("LOTUS_ACME_VALUE")
	path, body := "/set-txt", map[string]string{"host": name + ".", "value": value}
	if action == "cleanup" {
		path, body = "/clear-txt", map[string]string{"host": name + "."}
	}
	b, _ := json.Marshal(body)
	resp, err := http.Post("http://"+os.Getenv("LOTUS_TEST_DNS_MGMT")+path, "application/json", bytes.NewReader(b))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		fmt.Fprintln(os.Stderr, "management API:", resp.Status)
		os.Exit(1)
	}
	os.Exit(0)
}

// zoneDNS is a DNS server in front of the mock one that also answers SOA
// queries (the mock doesn't), so that LOTUS can find the zone of a name as it
// does with a real DNS service. Everything else goes to the mock.
func zoneDNS(t *testing.T, upstream string) string {
	t.Helper()
	handler := dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		q := r.Question[0]
		if q.Qtype == dns.TypeSOA {
			labels := dns.SplitDomainName(q.Name)
			if len(labels) >= 2 {
				zone := strings.Join(labels[len(labels)-2:], ".") + "."
				m := new(dns.Msg)
				m.SetReply(r)
				m.Authoritative = true
				if q.Name == zone { // the zone itself has an SOA; names below it have none
					m.Answer = append(m.Answer, &dns.SOA{
						Hdr: dns.RR_Header{Name: zone, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 60},
						Ns:  "ns." + zone, Mbox: "admin." + zone, Serial: 1, Refresh: 60, Retry: 60, Expire: 60, Minttl: 60,
					})
				} else {
					m.Ns = append(m.Ns, &dns.SOA{
						Hdr: dns.RR_Header{Name: zone, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 60},
						Ns:  "ns." + zone, Mbox: "admin." + zone, Serial: 1, Refresh: 60, Retry: 60, Expire: 60, Minttl: 60,
					})
				}
				w.WriteMsg(m)
				return
			}
		}
		resp, _, err := new(dns.Client).Exchange(r, upstream)
		if err != nil {
			m := new(dns.Msg)
			m.SetRcode(r, dns.RcodeServerFailure)
			w.WriteMsg(m)
			return
		}
		w.WriteMsg(resp)
	})
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	must(t, err)
	addr := pc.LocalAddr().String()
	srv := &dns.Server{PacketConn: pc, Handler: handler}
	go srv.ActivateAndServe()
	t.Cleanup(func() { srv.Shutdown() })
	return addr
}

func TestWildcardCertificateThroughDNS01(t *testing.T) {
	env := startPebble(t)
	self, err := os.Executable()
	must(t, err)
	t.Setenv("LOTUS_TEST_DNS_MGMT", env.chMgmt)
	dns := &model.DNSChallenge{Provider: "exec", Command: self, Resolvers: []string{zoneDNS(t, env.dns)}, SkipPropagationCheck: true}
	storage := filepath.Join(t.TempDir(), "certs")
	// A site and a wildcard alias, validated by DNS alone: the HTTP-based
	// validations can't prove a wildcard, and are switched off here.
	_, _ = lotusACMEWith(t, env, storage, []string{"dns-01"}, dns, "wild.test", "*.wild.test")

	leaf := waitCert(t, env, "anything.wild.test")
	if !slices.Contains(leaf.DNSNames, "*.wild.test") {
		t.Errorf("certificate names: %v", leaf.DNSNames)
	}
	roots, inters := env.pebbleRoots(t)
	st, err := handshake(t, env, "other.wild.test")
	must(t, err)
	ip := x509.NewCertPool()
	for _, c := range inters {
		ip.AddCert(c)
	}
	if _, err := st.PeerCertificates[0].Verify(x509.VerifyOptions{DNSName: "other.wild.test", Roots: roots, Intermediates: ip}); err != nil {
		t.Errorf("the wildcard certificate does not verify for another name: %v", err)
	}
	// The bare name has its own certificate (a wildcard does not cover it).
	waitCert(t, env, "wild.test")
	// Names outside the zone are not served.
	if _, err := handshake(t, env, "x.other.test"); err == nil {
		t.Error("a handshake for a name outside the wildcard succeeded")
	}
}

func runtimeUnix() bool { return os.PathSeparator == '/' }

func TestRenewCertificateWithPebble(t *testing.T) {
	env := startPebble(t)
	storage := filepath.Join(t.TempDir(), "certs")
	srv, _ := lotusACME(t, env, storage, nil, "renew.lotus-acme.test")
	first := waitCert(t, env, "renew.lotus-acme.test")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// A certificate issued a moment ago is not replaced: the CA's duplicate limit is not for a button to use up.
	if err := srv.RenewCertificate(ctx, "renew.lotus-acme.test"); !errors.Is(err, acme.ErrRenewedRecently) {
		t.Fatalf("renewing a fresh certificate: %v", err)
	}
	// A name that is not a managed one is refused without asking the CA.
	if err := srv.RenewCertificate(ctx, "not-a-site.test"); !errors.Is(err, acme.ErrNotManaged) {
		t.Errorf("renewing an unknown name: %v", err)
	}

	old := acme.MinRenewAge
	acme.MinRenewAge = 0
	defer func() { acme.MinRenewAge = old }()
	must(t, srv.RenewCertificate(ctx, "renew.lotus-acme.test"))

	// The next handshake gets the new certificate: renewing loads it, it does not wait for a restart.
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, err := handshake(t, env, "renew.lotus-acme.test")
		must(t, err)
		if st.PeerCertificates[0].SerialNumber.Cmp(first.SerialNumber) != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the server still serves the old certificate after a renewal")
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, c := range srv.Certificates() {
		if c.Name == "renew.lotus-acme.test" && !c.Ready {
			t.Errorf("the certificate is not ready after renewing: %+v", c)
		}
	}
}

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
