package admin

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/laikait/lotus/internal/control"
	"github.com/laikait/lotus/internal/sitefile"
)

const siteMain = `lotus_config_version 1

server {
    listen "http" {
        address "127.0.0.1:8080"
    }

    include "sites.d/*.conf"
}
`

// siteEnv is an env whose server has a real sites.d folder.
func siteEnv(t *testing.T) (*env, string, *http.Cookie, string) {
	t.Helper()
	e := newEnv(t)
	dir := t.TempDir()
	cfg := filepath.Join(dir, "main.conf")
	if err := os.WriteFile(cfg, []byte(siteMain), 0o644); err != nil {
		t.Fatal(err)
	}
	e.srv.opts.Sites = &sitefile.Store{Config: cfg}
	e.h = e.srv.Handler()
	cookie, csrf := e.signIn("ada", "correct horse battery")
	return e, dir, cookie, csrf
}

func TestAddEditAndRemoveASite(t *testing.T) {
	e, dir, cookie, csrf := siteEnv(t)
	web := filepath.ToSlash(filepath.Join(dir, "web"))

	rec := e.do("POST", "/v1/site-files", `{"name":"Example.com","aliases":["www.example.com"],"root":"`+web+`","create_root":true}`, csrf, cookie)
	if rec.Code != 200 {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	if e.backend.reloads != 1 {
		t.Errorf("reloads = %d: a change must be loaded", e.backend.reloads)
	}
	if _, err := os.Stat(filepath.Join(dir, "web", "index.html")); err != nil {
		t.Errorf("no sample page: %v", err)
	}

	rec = e.do("GET", "/v1/site-files", "", "", cookie)
	var list struct {
		Entries []struct {
			Name     string
			Editable bool
			Aliases  []string
		}
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Entries) != 1 || list.Entries[0].Name != "example.com" || !list.Entries[0].Editable {
		t.Fatalf("list: %s", rec.Body)
	}

	rec = e.do("PUT", "/v1/site-files/example.com", `{"aliases":["www.example.com","shop.example.com"],"root":"`+web+`","index":["index.php"]}`, csrf, cookie)
	if rec.Code != 200 {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	if text := string(e.srv.opts.Sites.Read("example.com")); !strings.Contains(text, "shop.example.com") || !strings.Contains(text, "index.php") {
		t.Errorf("file: %s", text)
	}

	if rec = e.do("DELETE", "/v1/site-files/example.com", "", csrf, cookie); rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if e.backend.reloads != 3 {
		t.Errorf("reloads = %d", e.backend.reloads)
	}
	if rec = e.do("DELETE", "/v1/site-files/example.com", "", csrf, cookie); rec.Code != 404 {
		t.Errorf("second delete: %d", rec.Code)
	}
}

func TestSiteChangesAreRefusedClearly(t *testing.T) {
	e, dir, cookie, csrf := siteEnv(t)
	web := filepath.ToSlash(filepath.Join(dir, "web"))

	for name, tc := range map[string]struct {
		body string
		code int
	}{
		"bad name":           {`{"name":"../evil","root":"` + web + `"}`, 422},
		"relative root":      {`{"name":"a.test","root":"www"}`, 422},
		"unknown field":      {`{"name":"a.test","root":"` + web + `","command":"rm"}`, 400},
		"not json":           {`name=a.test`, 400},
		"invalid as a whole": {`{"name":"a.test","root":"` + web + `","tls":"automatic"}`, 422},
	} {
		rec := e.do("POST", "/v1/site-files", tc.body, csrf, cookie)
		if rec.Code != tc.code {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if e.backend.reloads != 0 {
		t.Error("a refused change was loaded")
	}
	if list, _ := e.srv.opts.Sites.List(); len(list) != 0 {
		t.Errorf("files left: %+v", list)
	}

	// Automatic TLS without an ACME block is said in plain words, without the raw diagnostic and its file path.
	rec := e.do("POST", "/v1/site-files", `{"name":"t.test","root":"`+web+`","tls":"automatic"}`, csrf, cookie)
	var refused struct {
		Message     string
		Diagnostics []map[string]string
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &refused)
	if !strings.Contains(refused.Message, "Choose Off for plain HTTP") || len(refused.Diagnostics) != 0 {
		t.Errorf("automatic tls without acme: %s", rec.Body)
	}

	e.do("POST", "/v1/site-files", `{"name":"a.test","root":"`+web+`"}`, csrf, cookie)
	if rec := e.do("POST", "/v1/site-files", `{"name":"a.test","root":"`+web+`"}`, csrf, cookie); rec.Code != 409 {
		t.Errorf("a second site of the same name: %d", rec.Code)
	}
	if rec := e.do("PUT", "/v1/site-files/missing.test", `{"root":"`+web+`"}`, csrf, cookie); rec.Code != 404 {
		t.Errorf("edit of a missing site: %d", rec.Code)
	}
}

func TestARefusedReloadUndoesTheChange(t *testing.T) {
	e, dir, cookie, csrf := siteEnv(t)
	web := filepath.ToSlash(filepath.Join(dir, "web"))
	e.do("POST", "/v1/site-files", `{"name":"keep.test","root":"`+web+`"}`, csrf, cookie)
	before := string(e.srv.opts.Sites.Read("keep.test"))

	e.backend.reloadErr = control.NewError(control.CodeConfigInvalid, "cannot bind", nil)

	// A new site: no file afterwards.
	rec := e.do("POST", "/v1/site-files", `{"name":"new.test","root":"`+web+`"}`, csrf, cookie)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "undone") {
		t.Errorf("add: %d %s", rec.Code, rec.Body)
	}
	if e.srv.opts.Sites.Read("new.test") != nil {
		t.Error("the new site's file stayed")
	}
	// An edit: the old text is back.
	e.do("PUT", "/v1/site-files/keep.test", `{"root":"`+web+`","aliases":["x.test"]}`, csrf, cookie)
	if got := string(e.srv.opts.Sites.Read("keep.test")); got != before {
		t.Errorf("the edit stayed:\n%s", got)
	}
	// A delete: the file is back.
	e.do("DELETE", "/v1/site-files/keep.test", "", csrf, cookie)
	if got := string(e.srv.opts.Sites.Read("keep.test")); got != before {
		t.Errorf("the delete stayed:\n%s", got)
	}
}

func TestOnlyAnAdminChangesSites(t *testing.T) {
	e, dir, cookie, csrf := siteEnv(t)
	web := filepath.ToSlash(filepath.Join(dir, "web"))
	e.do("POST", "/v1/site-files", `{"name":"a.test","root":"`+web+`"}`, csrf, cookie)

	vcookie, vcsrf := e.signIn("vic", "another long secret")
	if rec := e.do("GET", "/v1/site-files", "", "", vcookie); rec.Code != 200 {
		t.Errorf("a viewer can't read: %d", rec.Code)
	}
	for _, c := range [][3]string{{"POST", "/v1/site-files", `{"name":"b.test","root":"` + web + `"}`}, {"PUT", "/v1/site-files/a.test", `{"root":"` + web + `"}`}, {"DELETE", "/v1/site-files/a.test", ""}} {
		if rec := e.do(c[0], c[1], c[2], vcsrf, vcookie); rec.Code != 403 {
			t.Errorf("viewer %s %s: %d", c[0], c[1], rec.Code)
		}
		if rec := e.do(c[0], c[1], c[2], "", cookie); rec.Code != 403 {
			t.Errorf("no token %s %s: %d", c[0], c[1], rec.Code)
		}
	}
	if e.srv.opts.Sites.Read("a.test") == nil || e.srv.opts.Sites.Read("b.test") != nil {
		t.Error("a refused call changed the folder")
	}
}

func TestTemplatesAndSettingsThroughTheAPI(t *testing.T) {
	e, _, cookie, csrf := siteEnv(t)

	rec := e.do("POST", "/v1/site-files", `{"name":"app.test","template":"proxy","proxy_url":"http://127.0.0.1:3000","rate_requests":30,"rate_per":"1m","security_headers":"basic","error_pages":{"404":"/nf.html"}}`, csrf, cookie)
	if rec.Code != 200 {
		t.Fatalf("add proxy site: %d %s", rec.Code, rec.Body)
	}
	rec = e.do("GET", "/v1/site-files", "", "", cookie)
	body := rec.Body.String()
	for _, want := range []string{`"templates":["static","php","wordpress","laravel","symfony","lphp","drupal","joomla","proxy"]`, `"php_backends":[]`, `"template":"proxy"`, `"proxy_url":"http://127.0.0.1:3000"`, `"editable":true`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
	// A bad value is refused with its reason and nothing is written.
	rec = e.do("POST", "/v1/site-files", `{"name":"bad.test","template":"proxy","proxy_url":"http://u:p@host"}`, csrf, cookie)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "proxy address") {
		t.Errorf("bad proxy: %d %s", rec.Code, rec.Body)
	}
	// A PHP template with no such backend is refused by the whole-configuration check.
	rec = e.do("POST", "/v1/site-files", `{"name":"php.test","template":"php","php_backend":"nope","root":"/srv/php"}`, csrf, cookie)
	if rec.Code != 422 {
		t.Errorf("unknown php backend: %d %s", rec.Code, rec.Body)
	}
}
