package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// explainConfig writes a site with a real document root and a few routes.
func explainConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "www")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("home"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := fmt.Sprintf(`lotus_config_version 1
server {
    listen "http" { address "127.0.0.1:8080" }
    site "example.test" {
        root "%s"
        error_page 404 "/index.html"
        route = "/old" { return 301 "/new" }
        route "/admin/" {
            return 200 "admin"
            match {
                client_ip !~ [ "10.0.0.0/8" ]
                return 403 "no"
            }
        }
        route "/" { try_files [ "{uri}", "{uri}/", "=404" ] }
    }
}
`, filepath.ToSlash(root))
	path := filepath.Join(dir, "main.conf")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConfigExplainText(t *testing.T) {
	cfg := explainConfig(t)

	code, out, errOut := runCLI("config", "explain", cfg, "http://example.test/old")
	if code != exitOK || errOut != "" {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	for _, want := range []string{"site: example.test (listener http)", `exact route = "/old"`, "result: 301 Moved Permanently, redirects to /new"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	// The file system is consulted: / is a directory with an index file.
	_, out, _ = runCLI("config", "explain", cfg, "http://example.test/")
	if !strings.Contains(out, "serves the file /index.html") {
		t.Errorf("index file:\n%s", out)
	}
	// A missing file goes through try_files =404 and then the error page.
	_, out, _ = runCLI("config", "explain", cfg, "http://example.test/nothing")
	if !strings.Contains(out, "error_page") || !strings.Contains(out, "result: 404, serves the file /index.html") {
		t.Errorf("error page:\n%s", out)
	}
}

func TestConfigExplainOptions(t *testing.T) {
	cfg := explainConfig(t)

	_, out, _ := runCLI("config", "explain", "--client-ip", "8.8.8.8", cfg, "http://example.test/admin/")
	if !strings.Contains(out, `result: 403 Forbidden, body "no"`) {
		t.Errorf("outside client:\n%s", out)
	}
	_, out, _ = runCLI("config", "explain", "--client-ip", "10.1.2.3", cfg, "http://example.test/admin/")
	if !strings.Contains(out, `body "admin"`) {
		t.Errorf("inside client:\n%s", out)
	}
	// A path plus a Host header.
	_, out, _ = runCLI("config", "explain", "--header", "Host: example.test", cfg, "/old")
	if !strings.Contains(out, "301") {
		t.Errorf("Host header form:\n%s", out)
	}
	// Without the file system the answer says so.
	_, out, _ = runCLI("config", "explain", "--no-files", cfg, "http://example.test/page.html")
	if !strings.Contains(out, "files not checked") || !strings.Contains(out, "assuming none of the unchecked files exist") {
		t.Errorf("--no-files:\n%s", out)
	}
}

func TestConfigExplainJSON(t *testing.T) {
	code, out, _ := runCLI("config", "explain", "--json", explainConfig(t), "http://example.test/old?x=1")
	if code != exitOK {
		t.Fatalf("code %d", code)
	}
	var rep struct {
		Site    string `json:"site"`
		Steps   []struct{ Phase, Detail string }
		Outcome struct {
			Kind     string
			Status   int
			Location string
		}
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if rep.Site != "example.test" || rep.Outcome.Kind != "redirect" || rep.Outcome.Status != 301 || rep.Outcome.Location != "/new" || len(rep.Steps) == 0 {
		t.Errorf("%+v", rep)
	}
}

func TestConfigExplainErrors(t *testing.T) {
	cfg := explainConfig(t)
	if code, _, errOut := runCLI("config", "explain", cfg); code != exitUsage || !strings.Contains(errOut, "usage:") {
		t.Errorf("missing URL: %d %q", code, errOut)
	}
	if code, _, errOut := runCLI("config", "explain", cfg, "/old"); code != exitUsage || !strings.Contains(errOut, "no host") {
		t.Errorf("no host: %d %q", code, errOut)
	}
	if code, _, errOut := runCLI("config", "explain", "--client-ip", "nope", cfg, "http://example.test/"); code != exitUsage || !strings.Contains(errOut, "--client-ip") {
		t.Errorf("bad client ip: %d %q", code, errOut)
	}
	if code, _, errOut := runCLI("config", "explain", cfg, "ftp://example.test/"); code != exitUsage || !strings.Contains(errOut, "http or https") {
		t.Errorf("bad scheme: %d %q", code, errOut)
	}
	if code, _, errOut := runCLI("config", "explain", "--header", "oops", cfg, "http://example.test/"); code != exitUsage {
		t.Errorf("bad header: %d %q", code, errOut)
	}
	bad := writeConfig(t, strings.Replace(goodConfig, `root "/var/www"`, `rot "/var/www"`, 1))
	code, out, errOut := runCLI("config", "explain", bad, "http://localhost/")
	if code != exitInvalidConfig || out != "" || !strings.Contains(errOut, "nothing to explain") {
		t.Errorf("invalid config: %d %q %q", code, out, errOut)
	}
}
