package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeHtaccess(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".htaccess")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const wordpressHtaccess = `# BEGIN WordPress
<IfModule mod_rewrite.c>
RewriteEngine On
RewriteBase /
RewriteRule ^index\.php$ - [L]
RewriteCond %{REQUEST_FILENAME} !-f
RewriteCond %{REQUEST_FILENAME} !-d
RewriteRule . /index.php [L]
</IfModule>
# END WordPress
`

func TestCompatHtaccessCheckAcceptsWordPress(t *testing.T) {
	code, out, errOut := runCLI("compat", "htaccess", "check", writeHtaccess(t, wordpressHtaccess))
	if code != exitOK || errOut != "" || !strings.Contains(out, "can apply this file as it is") {
		t.Errorf("code=%d\n%s\n%s", code, out, errOut)
	}
}

func TestCompatHtaccessCheckReportsWhatIsNotSupported(t *testing.T) {
	src := "RewriteEngine On\nphp_admin_value memory_limit 256M\nAuthType Digest\nOptions +FollowSymLinks\n<IfModule mod_pagespeed.c>\nModPagespeed on\n</IfModule>\n"
	code, out, _ := runCLI("compat", "htaccess", "check", writeHtaccess(t, src))
	if code != exitNotSupported {
		t.Errorf("exit code %d, want %d", code, exitNotSupported)
	}
	for _, want := range []string{"line 2", "php_admin_value", "UNSUPPORTED", "AuthType", "never skipped", "IGNORED", "mod_pagespeed", "can't apply"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "RewriteEngine") {
		t.Errorf("supported directives are listed only with --all:\n%s", out)
	}
	_, out, _ = runCLI("compat", "htaccess", "check", "--all", writeHtaccess(t, src))
	if !strings.Contains(out, "RewriteEngine") {
		t.Errorf("--all must list everything:\n%s", out)
	}
}

func TestCompatHtaccessCheckJSON(t *testing.T) {
	code, out, _ := runCLI("compat", "htaccess", "check", "--json", writeHtaccess(t, "Header set X 1\nAuthType Digest\n"))
	var res struct {
		Problems   int `json:"problems"`
		Directives []struct {
			Line      int    `json:"line"`
			Directive string `json:"directive"`
			Status    string `json:"status"`
			Sensitive bool   `json:"sensitive"`
		} `json:"directives"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if code != exitNotSupported || res.Problems != 1 || len(res.Directives) != 2 || res.Directives[1].Status != "unsupported" || !res.Directives[1].Sensitive {
		t.Errorf("code=%d %+v", code, res)
	}
}

func TestCompatHtaccessCheckAllowFlag(t *testing.T) {
	file := writeHtaccess(t, "RewriteEngine On\nHeader set X 1\n")
	// Flags may come after the file name.
	code, out, _ := runCLI("compat", "htaccess", "check", file, "--allow", "rewrite")
	if code != exitNotSupported || !strings.Contains(out, "NOT ALLOWED") {
		t.Errorf("code=%d\n%s", code, out)
	}
	if code, out, _ := runCLI("compat", "htaccess", "check", "--allow", "rewrite,headers", file); code != exitOK {
		t.Errorf("code=%d\n%s", code, out)
	}
}

func TestCompatHtaccessCheckUsageErrors(t *testing.T) {
	file := writeHtaccess(t, "RewriteEngine On\n")
	for name, args := range map[string][]string{
		"no file":            {"compat", "htaccess", "check"},
		"two files":          {"compat", "htaccess", "check", file, file},
		"unknown category":   {"compat", "htaccess", "check", "--allow", "bogus", file},
		"unknown subcommand": {"compat", "nginx", "check", file},
		"nothing":            {"compat"},
	} {
		if code, _, _ := runCLI(args...); code != exitUsage {
			t.Errorf("%s: exit code %d, want %d", name, code, exitUsage)
		}
	}
	if code, _, errOut := runCLI("compat", "htaccess", "check", filepath.Join(t.TempDir(), "nope")); code != exitNotFound || errOut == "" {
		t.Errorf("a missing file: %d %q", code, errOut)
	}
}

func TestCompatHtaccessCheckRefusesAnOversizedFile(t *testing.T) {
	code, out, _ := runCLI("compat", "htaccess", "check", writeHtaccess(t, strings.Repeat("# padding\n", 40000)))
	if code != exitNotSupported || !strings.Contains(out, "larger than") {
		t.Errorf("code=%d\n%.300s", code, out)
	}
}
