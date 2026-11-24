package acme

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libdns/libdns"
)

// The test binary doubles as the program of the exec DNS provider. It records
// how it was called in the file named by LOTUS_ACME_TEST_OUT, then behaves as
// LOTUS_ACME_TEST_MODE says.
func TestMain(m *testing.M) {
	if out := os.Getenv("LOTUS_ACME_TEST_OUT"); out != "" && len(os.Args) == 2 {
		rec := strings.Join([]string{
			os.Args[1],
			os.Getenv("LOTUS_ACME_ZONE"), os.Getenv("LOTUS_ACME_NAME"),
			os.Getenv("LOTUS_ACME_VALUE"), os.Getenv("LOTUS_ACME_TTL"),
		}, "|") + "\n"
		f, _ := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		f.WriteString(rec)
		f.Close()
		switch os.Getenv("LOTUS_ACME_TEST_MODE") {
		case "fail":
			os.Stderr.WriteString("the DNS API said: forbidden")
			os.Exit(3)
		case "noisy":
			os.Stdout.WriteString(strings.Repeat("x", 100000))
			os.Exit(1)
		case "hang":
			time.Sleep(time.Minute)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func txt(name, value string) libdns.Record {
	return libdns.RR{Type: "TXT", Name: name, Data: value, TTL: 60 * time.Second}
}

func execProv(t *testing.T, mode string) (*execProvider, string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "calls")
	t.Setenv("LOTUS_ACME_TEST_OUT", out)
	t.Setenv("LOTUS_ACME_TEST_MODE", mode)
	return &execProvider{command: self}, out
}

func calls(t *testing.T, file string) []string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestExecProviderPassesTheRecordInTheEnvironment(t *testing.T) {
	p, out := execProv(t, "")
	// A value that a shell would treat as commands is just a value here.
	value := `x"; touch /tmp/pwned; echo "$(id)`
	if _, err := p.AppendRecords(context.Background(), "example.com.", []libdns.Record{
		txt("_acme-challenge.www", value), txt("@", "apex"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.DeleteRecords(context.Background(), "example.com.", []libdns.Record{txt("_acme-challenge.www", value)}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"present|example.com|_acme-challenge.www.example.com|" + value + "|60",
		"present|example.com|example.com|apex|60",
		"cleanup|example.com|_acme-challenge.www.example.com|" + value + "|60",
	}
	got := calls(t, out)
	if len(got) != len(want) {
		t.Fatalf("calls: %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
}

func TestExecProviderReportsFailures(t *testing.T) {
	p, _ := execProv(t, "fail")
	_, err := p.AppendRecords(context.Background(), "example.com.", []libdns.Record{txt("_acme-challenge", "v")})
	if err == nil || !strings.Contains(err.Error(), "forbidden") || !strings.Contains(err.Error(), "_acme-challenge.example.com") {
		t.Errorf("the failure and its output should be reported: %v", err)
	}
	// A program that prints a lot can't fill the log.
	p, _ = execProv(t, "noisy")
	_, err = p.AppendRecords(context.Background(), "example.com.", []libdns.Record{txt("a", "v")})
	if err == nil || len(err.Error()) > 3000 {
		t.Errorf("error of %d bytes", len(err.Error()))
	}
	// It stops at the first failing record.
	p, out := execProv(t, "fail")
	p.AppendRecords(context.Background(), "example.com.", []libdns.Record{txt("a", "1"), txt("b", "2")})
	if n := len(calls(t, out)); n != 1 {
		t.Errorf("%d calls after a failure", n)
	}
}

func TestExecProviderStopsWhenCancelled(t *testing.T) {
	p, _ := execProv(t, "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := p.AppendRecords(ctx, "example.com.", []libdns.Record{txt("a", "v")})
	if err == nil || time.Since(start) > 20*time.Second {
		t.Errorf("a hanging program was waited for: %v after %v", err, time.Since(start))
	}
}

func TestDNSSettingsAreChecked(t *testing.T) {
	for name, d := range map[string]DNS{
		"unknown provider":    {Provider: "route66"},
		"cloudflare no token": {Provider: "cloudflare"},
		"exec no command":     {Provider: "exec"},
		"nothing":             {},
	} {
		if _, err := d.solver(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for name, d := range map[string]DNS{
		"cloudflare": {Provider: "cloudflare", APIToken: "t"},
		"exec":       {Provider: "exec", Command: "x"},
	} {
		if _, err := d.solver(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestPropagationSettings(t *testing.T) {
	d := DNS{Provider: "exec", Command: "x", PropagationDelay: 3 * time.Second, PropagationTimeout: time.Minute, Resolvers: []string{"1.1.1.1:53"}}
	s, err := d.solver()
	if err != nil {
		t.Fatal(err)
	}
	if s.PropagationDelay != 3*time.Second || s.PropagationTimeout != time.Minute || len(s.Resolvers) != 1 {
		t.Errorf("delay %v timeout %v resolvers %v", s.PropagationDelay, s.PropagationTimeout, s.Resolvers)
	}
	d.SkipPropagationCheck = true
	s, _ = d.solver()
	if s.PropagationTimeout != -1 {
		t.Errorf("the check is not skipped: %v", s.PropagationTimeout)
	}
}

func TestManagerWithDNSEnabledStarts(t *testing.T) {
	m, err := New(Config{Storage: storageDir(t), CA: "https://127.0.0.1:1/dir", Logger: quiet(),
		DNS: &DNS{Provider: "exec", Command: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if m.issuer.DNS01Solver == nil {
		t.Error("dns-01 is not offered although DNS is configured")
	}
	m.Close()
	// dns-01 is offered only when the challenges allow it.
	for challenges, want := range map[string]bool{"http-01": false, "dns-01": true, "http-01 dns-01": true} {
		m, err := New(Config{Storage: storageDir(t), CA: "https://127.0.0.1:1/dir", Logger: quiet(),
			Challenges: strings.Fields(challenges), DNS: &DNS{Provider: "exec", Command: "x"}})
		if err != nil {
			t.Fatal(err)
		}
		if got := m.issuer.DNS01Solver != nil; got != want {
			t.Errorf("challenges %q: dns-01 offered = %v", challenges, got)
		}
		m.Close()
	}
	if _, err := New(Config{Storage: storageDir(t), Logger: quiet(), DNS: &DNS{Provider: "cloudflare"}}); err == nil {
		t.Error("a cloudflare provider without a token was accepted")
	}
}
