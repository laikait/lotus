package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/control"
	"github.com/laikait/lotus/internal/server"
)

func ptr[T any](v T) *T { return &v }

func TestWriteCertsStatusesAndExitDecision(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	certs := []control.Certificate{
		{Name: "ok.test", Source: "file", Site: "ok.test", Domains: []string{"ok.test"}, Issuer: "Test CA", NotAfter: ptr(now.Add(60 * day)), Status: "ok"},
		{Name: "soon.test", Source: "acme", Managed: true, Domains: []string{"soon.test", "www.soon.test"}, Issuer: "Let's Encrypt R10", NotAfter: ptr(now.Add(5 * day))},
		{Name: "old.test", Source: "file", Domains: []string{"old.test"}, NotAfter: ptr(now.Add(-3*day - time.Hour))},
		{Name: "new.test", Source: "acme", Managed: true, Domains: []string{"new.test"}}, // not obtained yet
		{Name: "edge.test", Source: "file", Domains: []string{"edge.test"}, NotAfter: ptr(now.Add(14 * day))},
	}
	var out bytes.Buffer
	problems := writeCerts(&out, certs, now, 14*day, false)
	if problems != 3 {
		t.Errorf("problems = %d, want 3 (soon, old, new)", problems)
	}
	lines := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n")[1:] {
		lines[strings.Fields(l)[0]] = l
	}
	for name, want := range map[string][]string{
		"ok.test":   {"file", "ok", "2026-07-31", "60d", "Test CA"},
		"soon.test": {"acme (renewed by lotus)", "expiring", "5d", "Let's Encrypt R10", "soon.test, www.soon.test"},
		"old.test":  {"expired", "expired 4d ago"},
		"new.test":  {"pending"},
		"edge.test": {"ok", "14d"}, // exactly the window: not yet "expiring"
	} {
		for _, w := range want {
			if !strings.Contains(lines[name], w) {
				t.Errorf("%s: want %q in %q", name, w, lines[name])
			}
		}
	}

	// The status column itself, not just a word somewhere in the row.
	for _, re := range []string{
		`(?m)^ok\.test\s+file\s+ok\s+2026-07-31\s+60d`,
		`(?m)^soon\.test\s+acme \(renewed by lotus\)\s+expiring\s+2026-06-06\s+5d`,
		`(?m)^old\.test\s+file\s+expired\s+2026-05-29\s+expired 4d ago`,
		`(?m)^new\.test\s+acme \(renewed by lotus\)\s+pending\s+-\s+-`,
		`(?m)^edge\.test\s+file\s+ok\s+2026-06-15\s+14d`,
	} {
		if !regexp.MustCompile(re).MatchString(out.String()) {
			t.Errorf("no row matches %s in\n%s", re, out.String())
		}
	}

	// The warning window decides "expiring"; the server's own judgement is ignored.
	out.Reset()
	if p := writeCerts(&out, certs, now, 3*day, false); p != 2 {
		t.Errorf("with a 3 day window: %d problems, want 2 (old, new)", p)
	}
	out.Reset()
	if p := writeCerts(&out, certs, now, 90*day, false); p != 5 {
		t.Errorf("with a 90 day window: %d problems, want 5", p)
	}
}

func TestWriteCertsJSON(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	certs := []control.Certificate{
		{Name: "a.test", Source: "file", Domains: []string{"a.test"}, NotAfter: ptr(now.Add(48 * time.Hour))},
		{Name: "b.test", Source: "acme", Managed: true, Domains: []string{"b.test"}},
	}
	var out bytes.Buffer
	writeCerts(&out, certs, now, 14*24*time.Hour, true)
	var rows []struct {
		Name     string `json:"name"`
		Status   string `json:"status"`
		DaysLeft *int   `json:"days_left"`
		Managed  bool   `json:"managed"`
	}
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 2 {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if rows[0].Status != "expiring" || rows[0].DaysLeft == nil || *rows[0].DaysLeft != 2 || rows[1].Status != "pending" || rows[1].DaysLeft != nil || !rows[1].Managed {
		t.Errorf("%+v", rows)
	}
	out.Reset()
	writeCerts(&out, nil, now, time.Hour, true)
	if strings.TrimSpace(out.String()) != "[]" {
		t.Errorf("no certificates in JSON: %q", out.String())
	}
	out.Reset()
	if p := writeCerts(&out, nil, now, time.Hour, false); p != 0 || !strings.Contains(out.String(), "no certificates") {
		t.Errorf("%d %q", p, out.String())
	}
}

func TestControlCertsDescribeTheServersCertificates(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	got := toControlCerts([]server.CertInfo{
		{Name: "a.test", Site: "a.test", Source: "file", Domains: []string{"a.test", "b.test"}, Issuer: "Org CA", NotAfter: now.Add(90 * 24 * time.Hour), Ready: true},
		{Name: "c.test", Source: "acme", Domains: []string{"c.test"}, Issuer: "Let's Encrypt R10", NotAfter: now.Add(5 * 24 * time.Hour), Ready: true},
		{Name: "d.test", Source: "acme"}, // not obtained yet; no domains known
	}, now)
	if len(got) != 3 {
		t.Fatalf("%+v", got)
	}
	a, c, d := got[0], got[1], got[2]
	if a.Managed || a.Source != "file" || a.Site != "a.test" || a.Status != control.CertOK || a.NotAfter == nil || len(a.Domains) != 2 || a.Issuer != "Org CA" {
		t.Errorf("file certificate: %+v", a)
	}
	if !c.Managed || c.Source != "acme" || c.Status != control.CertExpiring || c.Site != "" {
		t.Errorf("acme certificate: %+v", c)
	}
	if !d.Managed || d.Status != control.CertPending || d.NotAfter != nil || len(d.Domains) != 1 || d.Domains[0] != "d.test" {
		t.Errorf("pending certificate: %+v", d)
	}
}

func TestDaysUntil(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		d    time.Duration
		want int
	}{
		{0, 0}, {time.Hour, 0}, {36 * time.Hour, 1}, {24 * time.Hour, 1}, {-time.Hour, -1}, {-24 * time.Hour, -1}, {-25 * time.Hour, -2},
	} {
		if got := daysUntil(now.Add(tc.d), now); got != tc.want {
			t.Errorf("%v: %d, want %d", tc.d, got, tc.want)
		}
	}
}

// certServer starts a server whose one site uses a certificate that is valid
// between the given times, and returns its control socket.
func certServer(t *testing.T, notBefore, notAfter time.Time) string {
	t.Helper()
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "cert.test", Organization: []string{"Test Org"}},
		DNSNames: []string{"cert.test", "www.cert.test"}, NotBefore: notBefore, NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	certPath, keyPath := filepath.Join(dir, "c.pem"), filepath.Join(dir, "c.key")
	os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600)
	root := filepath.Join(dir, "www")
	os.MkdirAll(root, 0o755)

	sock := filepath.Join(socketDir(t), "ctl", "control.sock")
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	cfg := fmt.Sprintf(`lotus_config_version 1
server {
    listen "https" {
        address "%s"
        protocol https
    }
    control { socket "%s" }
    site "cert.test" {
        aliases [ "www.cert.test" ]
        root "%s"
        tls manual
        certificate "%s"
        certificate_key "%s"
    }
}
`, addr, filepath.ToSlash(sock), filepath.ToSlash(root), filepath.ToSlash(certPath), filepath.ToSlash(keyPath))
	cfgPath := filepath.Join(dir, "main.lotus")
	os.WriteFile(cfgPath, []byte(cfg), 0o644)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	ready := make(chan struct{})
	var stdout, stderr safeBuf
	go func() {
		done <- runServer(ctx, []string{"-config", cfgPath}, &stdout, &stderr, func(map[string]net.Addr) { close(ready) }, nil)
	}()
	select {
	case <-ready:
	case code := <-done:
		t.Fatalf("exited early with %d:\n%s", code, stderr.String())
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not become ready")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	})
	return sock
}

func TestCertsCommandAgainstARunningServer(t *testing.T) {
	now := time.Now()
	sock := certServer(t, now.Add(-time.Hour), now.Add(60*24*time.Hour+time.Hour))
	code, out, errOut := runCLI("certs", "--control", sock)
	if code != exitOK || errOut != "" {
		t.Fatalf("code=%d stderr=%q\n%s", code, errOut, out)
	}
	for _, want := range []string{"cert.test", "file", "ok", "60d", "Test Org", "cert.test, www.cert.test"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// A longer warning window makes the same certificate a problem, and the exit code says so.
	code, out, errOut = runCLI("certs", "--control", sock, "--warn-days", "90")
	if code != exitCertProblem || !strings.Contains(errOut, "1 certificate(s) need attention") || !strings.Contains(out, "expiring") {
		t.Errorf("--warn-days 90: code=%d stderr=%q\n%s", code, errOut, out)
	}
	code, out, _ = runCLI("certs", "--control", sock, "--json")
	var rows []struct {
		Name     string `json:"name"`
		Source   string `json:"source"`
		Status   string `json:"status"`
		DaysLeft int    `json:"days_left"`
	}
	if code != exitOK || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 || rows[0].Name != "cert.test" || rows[0].Source != "file" || rows[0].DaysLeft != 60 {
		t.Errorf("json: %d %s", code, out)
	}
}

func TestCertsCommandReportsExpiredAndExpiringCertificates(t *testing.T) {
	now := time.Now()
	for name, tc := range map[string]struct {
		notAfter time.Time
		want     string
	}{
		"expired":  {now.Add(-48 * time.Hour), "expired"},
		"expiring": {now.Add(5 * 24 * time.Hour), "expiring"},
	} {
		sock := certServer(t, now.Add(-100*24*time.Hour), tc.notAfter)
		code, out, errOut := runCLI("certs", "--control", sock)
		if code != exitCertProblem || !strings.Contains(out, tc.want) || !strings.Contains(errOut, "need attention") {
			t.Errorf("%s: code=%d stdout=%q stderr=%q", name, code, out, errOut)
		}
	}
}

func TestCertsCommandWithoutCertificatesOrServer(t *testing.T) {
	c := startControlled(t) // one plain-HTTP site
	code, out, errOut := c.cli("certs")
	if code != exitOK || !strings.Contains(out, "no certificates") || errOut != "" {
		t.Errorf("code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	if code, _, errOut := runCLI("certs", "--control", filepath.Join(t.TempDir(), "nobody.sock")); code != exitUnreachable || !strings.Contains(errOut, "no running server") {
		t.Errorf("no server: code=%d %q", code, errOut)
	}
	if code, _, _ := runCLI("certs", "--warn-days", "-1"); code != exitUsage {
		t.Errorf("negative warn days: %d", code)
	}
	if code, _, _ := runCLI("certs", "extra"); code != exitUsage {
		t.Errorf("extra argument: %d", code)
	}
}
