package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const untidy = "lotus_config_version 1\nserver{ listen \"http\" {address \"127.0.0.1:8080\"}\n}\n"
const tidy = "lotus_config_version 1\nserver {\n    listen \"http\" {\n        address \"127.0.0.1:8080\"\n    }\n}\n"

func writeLotus(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "main.conf")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConfigFormatPrints(t *testing.T) {
	code, out, errOut := runCLI("config", "format", writeLotus(t, untidy))
	if code != exitOK || out != tidy || errOut != "" {
		t.Errorf("code=%d\n%q\n%s", code, out, errOut)
	}
	if code, out, _ := runCLI("config", "fmt", writeLotus(t, tidy)); code != exitOK || out != tidy {
		t.Errorf("fmt alias on a formatted file: %d %q", code, out)
	}
}

func TestConfigFormatCheck(t *testing.T) {
	bad, good := writeLotus(t, untidy), writeLotus(t, tidy)
	code, out, _ := runCLI("config", "format", "--check", good, bad)
	if code != exitNotFormatted || strings.TrimSpace(out) != bad {
		t.Errorf("code=%d out=%q", code, out)
	}
	if code, out, _ := runCLI("config", "format", "--check", good); code != exitOK || out != "" {
		t.Errorf("code=%d out=%q", code, out)
	}
	if data, _ := os.ReadFile(bad); string(data) != untidy {
		t.Error("--check must not change the file")
	}
}

func TestConfigFormatWrite(t *testing.T) {
	p := writeLotus(t, untidy)
	code, out, _ := runCLI("config", "format", "-w", p)
	if code != exitOK || !strings.Contains(out, "formatted") {
		t.Errorf("code=%d %q", code, out)
	}
	if data, _ := os.ReadFile(p); string(data) != tidy {
		t.Errorf("%q", data)
	}
	// A second run changes nothing and says nothing.
	if code, out, _ := runCLI("config", "format", "--write", p); code != exitOK || out != "" {
		t.Errorf("code=%d %q", code, out)
	}
	// The formatted file is still valid.
	if code, _, e := runCLI("config", "validate", p); code != exitOK {
		t.Errorf("validate: %d %s", code, e)
	}
}

func TestConfigFormatErrors(t *testing.T) {
	bad := writeLotus(t, "server {\n  listen\n")
	if code, _, errOut := runCLI("config", "format", bad); code != exitInvalidConfig || !strings.Contains(errOut, "syntax errors") {
		t.Errorf("syntax error: %d %s", code, errOut)
	}
	if data, _ := os.ReadFile(bad); string(data) != "server {\n  listen\n" {
		t.Error("a file with errors is never rewritten")
	}
	runCLI("config", "format", "-w", bad)
	if data, _ := os.ReadFile(bad); string(data) != "server {\n  listen\n" {
		t.Error("-w must not rewrite a file with errors")
	}
	if code, _, _ := runCLI("config", "format", filepath.Join(t.TempDir(), "none.conf")); code != exitNotFound {
		t.Errorf("missing: %d", code)
	}
	if code, _, _ := runCLI("config", "format"); code != exitUsage {
		t.Errorf("no file: %d", code)
	}
	if code, _, _ := runCLI("config", "format", "--check", "--write", "x"); code != exitUsage {
		t.Errorf("both flags: %d", code)
	}
	if code, _, _ := runCLI("config", "format", "-w", "-"); code != exitUsage {
		t.Errorf("stdin with -w: %d", code)
	}
}

func TestConfigFormatSeveralFilesAreLabelled(t *testing.T) {
	a, b := writeLotus(t, untidy), writeLotus(t, tidy)
	_, out, _ := runCLI("config", "format", a, b)
	if strings.Count(out, "# ") != 2 || !strings.Contains(out, "# "+a) {
		t.Errorf("%q", out)
	}
}

func TestConfigMigrate(t *testing.T) {
	p := writeLotus(t, tidy)
	code, out, _ := runCLI("config", "migrate", p)
	if code != exitOK || !strings.Contains(out, "current one; nothing to migrate") {
		t.Errorf("%d %q", code, out)
	}
	if code, _, _ := runCLI("config", "migrate", "--check", p); code != exitOK {
		t.Errorf("--check on a current file: %d", code)
	}
	newer := writeLotus(t, "lotus_config_version 9\n")
	if code, _, errOut := runCLI("config", "migrate", newer); code != exitInvalidConfig || !strings.Contains(errOut, "newer LOTUS") {
		t.Errorf("%d %s", code, errOut)
	}
	if code, _, errOut := runCLI("config", "migrate", writeLotus(t, "server {\n}\n")); code != exitInvalidConfig || !strings.Contains(errOut, "lotus_config_version") {
		t.Errorf("%d %s", code, errOut)
	}
	if code, _, _ := runCLI("config", "migrate", writeLotus(t, "a {\n")); code != exitInvalidConfig {
		t.Errorf("syntax: %d", code)
	}
	if code, _, _ := runCLI("config", "migrate", filepath.Join(t.TempDir(), "none")); code != exitNotFound {
		t.Errorf("missing: %d", code)
	}
	if code, _, _ := runCLI("config", "migrate"); code != exitUsage {
		t.Errorf("no file: %d", code)
	}
	if code, _, _ := runCLI("config", "migrate", "--check", "--write", p); code != exitUsage {
		t.Errorf("both: %d", code)
	}
}
