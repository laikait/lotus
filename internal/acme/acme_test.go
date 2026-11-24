package acme

import (
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// storageDir is a storage directory removed at the end of the test. Issuance
// goroutines of certmagic may still be writing for a moment after Close, so
// failing to remove it is not an error.
func storageDir(t *testing.T) string {
	dir, err := os.MkdirTemp("", "lotus-acme")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "certs")
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestStorageMustBeUsable(t *testing.T) {
	if _, err := New(Config{Logger: quiet()}); err == nil {
		t.Error("no storage directory was accepted")
	}
	file := filepath.Join(t.TempDir(), "afile")
	os.WriteFile(file, []byte("x"), 0o600)
	if _, err := New(Config{Storage: filepath.Join(file, "sub"), Logger: quiet()}); err == nil {
		t.Error("a storage path below a file was accepted")
	}
	m, err := New(Config{Storage: storageDir(t), CA: "https://127.0.0.1:1/dir", Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	m.Close()
}

func TestCARootMustBeACertificate(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"junk": "not pem", "empty": ""} {
		f := filepath.Join(dir, name)
		os.WriteFile(f, []byte(content), 0o600)
		if _, err := New(Config{Storage: filepath.Join(dir, "s"), CARoot: f, Logger: quiet()}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := New(Config{Storage: filepath.Join(dir, "s"), CARoot: filepath.Join(dir, "missing"), Logger: quiet()}); err == nil {
		t.Error("a missing file was accepted")
	}
}

func TestOnlyNamesACAWouldIssueForAreManaged(t *testing.T) {
	m, err := New(Config{Storage: storageDir(t), CA: "https://127.0.0.1:1/dir", Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	skipped := m.Manage([]string{"localhost", "127.0.0.1", "intranet", "::1", "printer.local", "example.com"})
	for _, n := range []string{"localhost", "127.0.0.1", "intranet", "::1"} {
		if skipped[n] == "" {
			t.Errorf("%q would be sent to a public CA", n)
		}
	}
	if skipped["example.com"] != "" {
		t.Errorf("a public name was skipped: %q", skipped["example.com"])
	}
	// Names already managed are not started twice.
	if again := m.Manage([]string{"example.com"}); len(again) != 0 {
		t.Errorf("%v", again)
	}
}

func TestChallengeDetection(t *testing.T) {
	m, err := New(Config{Storage: storageDir(t), Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	yes := httptest.NewRequest("GET", "/.well-known/acme-challenge/token123", nil)
	if !LooksLikeHTTPChallenge(yes) {
		t.Error("the challenge path was not recognised")
	}
	for _, p := range []string{"/", "/.well-known/security.txt", "/acme-challenge/x"} {
		if LooksLikeHTTPChallenge(httptest.NewRequest("GET", p, nil)) {
			t.Errorf("%s looks like a challenge", p)
		}
	}
	// A challenge nobody asked for is not answered with anything useful.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://example.com/.well-known/acme-challenge/unknown", nil)
	if m.HandleHTTPChallenge(rec, req) && rec.Code == http.StatusOK {
		t.Errorf("an unknown token was answered: %d %q", rec.Code, rec.Body.String())
	}

	if !IsChallenge(&tls.ClientHelloInfo{SupportedProtos: []string{ALPNProtocol}}) {
		t.Error("an acme-tls/1 handshake was not recognised")
	}
	if IsChallenge(&tls.ClientHelloInfo{SupportedProtos: []string{"h2", "http/1.1"}}) {
		t.Error("an ordinary handshake was taken for a challenge")
	}
	// No certificate exists, so a handshake gets an error, not an issuance.
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	if _, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: "never-seen.example.com", Conn: c1}); err == nil {
		t.Error("a certificate appeared for a name that is not managed")
	}
}
