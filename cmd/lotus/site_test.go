package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const siteTestMain = `lotus_config_version 1

server {
    listen "http" {
        address "127.0.0.1:8080"
    }

    include "sites.d/*.lotus"
}
`

func TestSiteAddListRemove(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "main.lotus")
	if err := os.WriteFile(cfg, []byte(siteTestMain), 0o644); err != nil {
		t.Fatal(err)
	}
	web := filepath.Join(dir, "web")

	var out, errb bytes.Buffer
	code := run([]string{"site", "add", "Example.com", "--config", cfg, "--root", web, "--aliases", "www.example.com"}, &out, &errb)
	if code != exitOK {
		t.Fatalf("add: exit %d: %s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(web, "index.html")); err != nil {
		t.Errorf("no sample page: %v", err)
	}
	text, _ := os.ReadFile(filepath.Join(dir, "sites.d", "example.com.lotus"))
	if !strings.Contains(string(text), `aliases [ "www.example.com" ]`) {
		t.Errorf("site file: %s", text)
	}

	// The same name again is refused, and the first one stays.
	errb.Reset()
	if code := run([]string{"site", "add", "example.com", "--config", cfg, "--root", web}, &out, &errb); code == exitOK {
		t.Error("a second add of the same name worked")
	}

	out.Reset()
	if code := run([]string{"site", "list", "--config", cfg}, &out, &errb); code != exitOK || strings.TrimSpace(out.String()) != "example.com" {
		t.Errorf("list: %d %q", code, out.String())
	}

	// A name that would leave the folder is refused.
	errb.Reset()
	if code := run([]string{"site", "add", "../evil", "--config", cfg}, &out, &errb); code == exitOK {
		t.Error("a path as a name was accepted")
	}

	if code := run([]string{"site", "remove", "example.com", "--config", cfg}, &out, &errb); code != exitOK {
		t.Errorf("remove: %d %s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "sites.d", "example.com.lotus")); err == nil {
		t.Error("the site file is still there")
	}
}
