package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeNginx(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "nginx.conf")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const cleanNginx = `http {
    server {
        listen 8080;
        server_name example.test;
        root /srv/example;
        location /api/ { proxy_pass http://127.0.0.1:9000; proxy_set_header Host $host; }
    }
}
`

const messyNginx = `http {
    server {
        listen 8080;
        server_name example.test;
        root /srv/example;
        location /files/ { alias /data/; }
        location /p/ { proxy_pass http://127.0.0.1:9000/; }
        sendfile on;
    }
}
`

func TestCompatNginxConvertCleanConfiguration(t *testing.T) {
	code, out, errOut := runCLI("compat", "nginx", "convert", writeNginx(t, cleanNginx))
	if code != exitOK {
		t.Fatalf("code=%d\n%s\n%s", code, out, errOut)
	}
	for _, want := range []string{"lotus_config_version 1", `site "example.test"`, `proxy "proxy-127-0-0-1-9000"`} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(errOut, "0 unsupported, 0 approximated") || !strings.Contains(errOut, "everything was translated") {
		t.Errorf("the report goes to stderr:\n%s", errOut)
	}
	// What was written is a configuration LOTUS accepts.
	cfg := filepath.Join(t.TempDir(), "out.conf")
	if err := os.WriteFile(cfg, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, o, e := runCLI("config", "validate", cfg); code != exitOK {
		t.Errorf("validate: %d %s %s", code, o, e)
	}
}

func TestCompatNginxConvertReportsWhatNeedsReview(t *testing.T) {
	code, out, errOut := runCLI("compat", "nginx", "convert", writeNginx(t, messyNginx))
	if code != exitNotSupported {
		t.Errorf("exit code %d, want %d", code, exitNotSupported)
	}
	for _, want := range []string{"1 unsupported, 1 approximated, 1 ignored", "nginx.conf:6", "alias", "UNSUPPORTED", "proxy_pass", "APPROXIMATED", "2 place(s) need your review"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("the report lacks %q:\n%s", want, errOut)
		}
	}
	if strings.Contains(errOut, "sendfile") {
		t.Errorf("ignored directives are listed only with --all:\n%s", errOut)
	}
	// The same places are marked in the output, where they were.
	if !strings.Contains(out, "# nginx line 6, unsupported") || !strings.Contains(out, "alias /data/;") {
		t.Errorf("the output lacks the comment:\n%s", out)
	}
	_, _, errOut = runCLI("compat", "nginx", "convert", "--all", writeNginx(t, messyNginx))
	if !strings.Contains(errOut, "sendfile") || !strings.Contains(errOut, "IGNORED") || !strings.Contains(errOut, "translated") {
		t.Errorf("--all lists everything:\n%s", errOut)
	}
}

func TestCompatNginxConvertWritesAFile(t *testing.T) {
	src := writeNginx(t, cleanNginx)
	dst := filepath.Join(t.TempDir(), "site.conf")
	code, out, errOut := runCLI("compat", "nginx", "convert", "-o", dst, src)
	if code != exitOK || errOut != "" || !strings.Contains(out, "written to "+dst) {
		t.Fatalf("code=%d\nstdout=%s\nstderr=%s", code, out, errOut)
	}
	data, err := os.ReadFile(dst)
	if err != nil || !strings.Contains(string(data), `site "example.test"`) {
		t.Fatalf("%v %s", err, data)
	}
	if strings.Contains(out, "lotus_config_version") {
		t.Errorf("with -o the configuration is not printed:\n%s", out)
	}
	// An existing file is not overwritten unless asked.
	if code, _, errOut := runCLI("compat", "nginx", "convert", "--output", dst, src); code != exitUsage || !strings.Contains(errOut, "--force") {
		t.Errorf("code=%d %s", code, errOut)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runCLI("compat", "nginx", "convert", "-o", dst, "--force", src); code != exitOK {
		t.Errorf("--force: %d", code)
	}
	if data, _ := os.ReadFile(dst); string(data) == "old" {
		t.Error("--force must overwrite")
	}
}

func TestCompatNginxConvertJSON(t *testing.T) {
	code, out, _ := runCLI("compat", "nginx", "convert", "--json", writeNginx(t, messyNginx))
	var res struct {
		Valid        bool   `json:"valid"`
		Config       string `json:"config"`
		Unsupported  int    `json:"unsupported"`
		Approximated int    `json:"approximated"`
		Ignored      int    `json:"ignored"`
		Issues       []struct {
			File      string `json:"file"`
			Line      int    `json:"line"`
			Directive string `json:"directive"`
			Status    string `json:"status"`
		} `json:"issues"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if code != exitNotSupported || !res.Valid || res.Unsupported != 1 || res.Approximated != 1 || res.Ignored != 1 ||
		!strings.Contains(res.Config, "lotus_config_version") || len(res.Issues) != 3 || res.Issues[0].File != "nginx.conf" {
		t.Errorf("code=%d %+v", code, res)
	}
}

func TestCompatNginxConvertErrors(t *testing.T) {
	if code, _, errOut := runCLI("compat", "nginx", "convert", filepath.Join(t.TempDir(), "missing.conf")); code != exitNotFound {
		t.Errorf("missing file: %d %s", code, errOut)
	}
	if code, _, errOut := runCLI("compat", "nginx", "convert", writeNginx(t, "http { server {")); code != exitInvalidConfig || !strings.Contains(errOut, "never closed") {
		t.Errorf("syntax error: %d %s", code, errOut)
	}
	if code, _, _ := runCLI("compat", "nginx", "convert"); code != exitUsage {
		t.Errorf("no file: %d", code)
	}
	if code, _, _ := runCLI("compat", "nginx", "convert", "a", "b"); code != exitUsage {
		t.Errorf("two files: %d", code)
	}
	if code, _, errOut := runCLI("compat", "nginx"); code != exitUsage || !strings.Contains(errOut, "convert") {
		t.Errorf("no subcommand: %d %s", code, errOut)
	}
	if code, _, _ := runCLI("compat", "nginx", "convert", "--bogus", "x"); code != exitUsage {
		t.Errorf("bad flag: %d", code)
	}
	// The usage of `compat` itself mentions both tools.
	if _, _, errOut := runCLI("compat"); !strings.Contains(errOut, "nginx convert") || !strings.Contains(errOut, "htaccess check") {
		t.Errorf("usage: %s", errOut)
	}
}

func TestCompatNginxFlagsMayFollowTheFile(t *testing.T) {
	code, out, _ := runCLI("compat", "nginx", "convert", writeNginx(t, cleanNginx), "--json")
	if code != exitOK || !strings.Contains(out, `"valid": true`) {
		t.Errorf("code=%d %s", code, out)
	}
}
