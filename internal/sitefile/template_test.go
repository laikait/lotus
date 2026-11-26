package sitefile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mainWithPHP = `lotus_config_version 1

server {
    listen "http" {
        address "127.0.0.1:8080"
    }

    php_backend "app" {
        fastcgi_pass "127.0.0.1:9000"
    }

    include "sites.d/*.conf"
}
`

func phpStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "main.conf")
	if err := os.WriteFile(cfg, []byte(mainWithPHP), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Store{Config: cfg}, dir
}

// Every template makes a file that the whole-configuration check accepts and that reads back as the same spec.
func TestEveryTemplateIsValidAndRoundTrips(t *testing.T) {
	st, dir := phpStore(t)
	full := func(name, tmpl string) Spec {
		return Spec{
			Name: name, Aliases: []string{"www." + name}, Root: filepath.ToSlash(filepath.Join(dir, "www", name)),
			Template: tmpl, PHPBackend: "app", ProxyURL: "http://127.0.0.1:3000/",
			RateRequests: 20, RatePer: "1m", RateBurst: 40, Compression: "on", SecurityHeaders: "basic",
			Symlinks: "deny", Dotfiles: "deny", HTAccess: true,
			ErrorPages: map[string]string{"404": "/404.html", "500": "/oops.html"},
			AccessLog:  filepath.ToSlash(filepath.Join(dir, "logs", name+".access.log")),
			ErrorLog:   filepath.ToSlash(filepath.Join(dir, "logs", name+".error.log")),
		}
	}
	for _, tmpl := range Templates {
		name := tmpl + ".test"
		sp, err := st.Add(full(name, tmpl), true)
		if err != nil {
			var inv *InvalidError
			if errors.As(err, &inv) {
				t.Fatalf("%s: %v %+v", tmpl, err, inv.Diagnostics)
			}
			t.Fatalf("%s: %v", tmpl, err)
		}
		text := string(st.Read(name))
		got, ok := Parse(text)
		if !ok {
			t.Fatalf("%s: a file made here can't be read back:\n%s", tmpl, text)
		}
		if Render(got) != text {
			t.Errorf("%s: the text changed on a round trip", tmpl)
		}
		wantPage := "/404.html"
		if tmpl == "proxy" {
			wantPage = "" // a proxy site serves no files, so it keeps no error pages
		}
		if got.Template != tmpl || got.RateRequests != 20 || got.ErrorPages["404"] != wantPage || got.Compression != "on" {
			t.Errorf("%s: %+v", tmpl, got)
		}
		_ = sp
	}
	// The template is what decides the routes.
	if text := string(st.Read("wordpress.test")); !strings.Contains(text, `"/wp-content/uploads/"`) || !strings.Contains(text, `php "app"`) {
		t.Errorf("wordpress:\n%s", text)
	}
	if text := string(st.Read("laravel.test")); !strings.Contains(text, `/public"`) {
		t.Errorf("laravel serves public/:\n%s", text)
	}
	text := string(st.Read("proxy.test"))
	if !strings.Contains(text, `upstream "site-proxy.test"`) || !strings.Contains(text, `backend "http://127.0.0.1:3000"`) || !strings.Contains(text, `proxy "site-proxy.test"`) {
		t.Errorf("proxy:\n%s", text)
	}
	// Web directories: PHP templates get a sample page, WordPress and proxy do not.
	if _, err := os.Stat(filepath.Join(dir, "www", "php.test", "index.php")); err != nil {
		t.Errorf("php sample page: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "www", "laravel.test", "public", "index.php")); err != nil {
		t.Errorf("laravel sample page: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "www", "proxy.test")); err == nil {
		t.Error("a proxy site has no web directory")
	}
}

func TestSettingsAreChecked(t *testing.T) {
	good := Spec{Name: "ok.test", Root: "/srv/ok", Template: "php", PHPBackend: "app"}
	if err := Validate(Normalize(good)); err != nil {
		t.Fatalf("good spec: %v", err)
	}
	bad := map[string]func(*Spec){
		"template":       func(s *Spec) { s.Template = "perl" },
		"php no backend": func(s *Spec) { s.PHPBackend = "" },
		"backend quote":  func(s *Spec) { s.PHPBackend = `a"b` },
		"proxy url":      func(s *Spec) { s.Template, s.ProxyURL = "proxy", "http://user:pw@host:80" },
		"proxy path":     func(s *Spec) { s.Template, s.ProxyURL = "proxy", "http://host/app" },
		"proxy scheme":   func(s *Spec) { s.Template, s.ProxyURL = "proxy", "file:///etc/passwd" },
		"rate period":    func(s *Spec) { s.RateRequests, s.RatePer = 5, "2d" },
		"compression":    func(s *Spec) { s.Compression = "maybe" },
		"headers":        func(s *Spec) { s.SecurityHeaders = "strict" },
		"symlinks":       func(s *Spec) { s.Symlinks = "yes" },
		"dotfiles":       func(s *Spec) { s.Dotfiles = "sometimes" },
		"error status":   func(s *Spec) { s.ErrorPages = map[string]string{"200": "/x.html"} },
		"error target":   func(s *Spec) { s.ErrorPages = map[string]string{"404": "/a\"b"} },
		"error up":       func(s *Spec) { s.ErrorPages = map[string]string{"404": "/../x"} },
		"log relative":   func(s *Spec) { s.AccessLog = "logs/a.log" },
		"log quote":      func(s *Spec) { s.ErrorLog = `/var/log/a".log` },
	}
	for name, f := range bad {
		sp := good
		f(&sp)
		if err := Validate(Normalize(sp)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A file made before the spec line existed is still editable, and the next save upgrades it.
func TestAnOlderFileIsStillEditable(t *testing.T) {
	st, _ := newStore(t)
	legacy := Marker + "\nsite \"old.test\" {\n    root \"/srv/old\"\n    index [ \"index.html\" ]\n    tls off\n}\n"
	if err := os.MkdirAll(st.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.File("old.test"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	list, err := st.List()
	if err != nil || len(list) != 1 || !list[0].Editable || list[0].Template != "static" {
		t.Fatalf("list: %v %+v", err, list)
	}
	if _, err := st.Update("old.test", Spec{Root: "/srv/old", Compression: "on"}); err != nil {
		t.Fatal(err)
	}
	if text := string(st.Read("old.test")); !strings.Contains(text, SpecPrefix) {
		t.Errorf("not upgraded:\n%s", text)
	}
}

// Any change to the text, even in the spec line, makes the file "written by hand".
func TestAHandEditedV2FileIsReadOnly(t *testing.T) {
	text := Render(Normalize(Spec{Name: "a.test", Root: "/srv/a", Compression: "on"}))
	for name, edited := range map[string]string{
		"extra directive": strings.Replace(text, "    tls off\n", "    tls off\n    max_something 1\n", 1),
		"other value":     strings.Replace(text, `"on"`, `"off"`, 1),
		"no spec line":    strings.Replace(text, SpecPrefix, "# note: ", 1),
		"unknown field":   strings.Replace(text, `{"name"`, `{"evil":1,"name"`, 1),
	} {
		if _, ok := Parse(edited); ok {
			t.Errorf("%s: parsed as editable", name)
		}
	}
}
