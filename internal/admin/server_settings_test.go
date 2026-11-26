package admin

import (
	"os"
	"strings"
	"testing"

	"github.com/laikait/lotus/internal/control"
	"github.com/laikait/lotus/internal/sitefile"
)

func TestPHPPoolsAddEditDeleteAndRestart(t *testing.T) {
	e, dir, cookie, csrf := siteEnv(t)
	restarted := []string{}
	e.srv.opts.PHPRestart = func(n string) bool { restarted = append(restarted, n); return n == "app" }

	rec := e.do("PUT", "/v1/php-pools/app", `{"command":"C:\\xampp\\php\\php-cgi.exe","processes":4,"max_requests":500,"timeout":30}`, csrf, cookie)
	if rec.Code != 200 {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	rec = e.do("GET", "/v1/php-pools", "", "", cookie)
	if !strings.Contains(rec.Body.String(), `"command":"C:/xampp/php/php-cgi.exe"`) || !strings.Contains(rec.Body.String(), `"can_restart":true`) {
		t.Errorf("list: %s", rec.Body)
	}
	// It is a backend sites can pick.
	if rec = e.do("GET", "/v1/site-files", "", "", cookie); !strings.Contains(rec.Body.String(), `"php_backends":["app"]`) {
		t.Errorf("site form backends: %s", rec.Body)
	}
	// Change it.
	if rec = e.do("PUT", "/v1/php-pools/app", `{"command":"php-cgi","processes":8,"timeout":60}`, csrf, cookie); rec.Code != 200 {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body)
	}
	if text, _ := os.ReadFile(e.srv.opts.Sites.File(sitefile.ServerFile)); !strings.Contains(string(text), "processes 8") || strings.Contains(string(text), "max_requests") {
		t.Errorf("file:\n%s", text)
	}
	// Bad values are refused with the reason.
	for _, body := range []string{`{"command":"php cgi","processes":1}`, `{"command":"php-cgi","processes":0}`, `{"command":"php-cgi","processes":2,"php_ini":"x.ini"}`} {
		if rec = e.do("PUT", "/v1/php-pools/app", body, csrf, cookie); rec.Code != 422 {
			t.Errorf("%s: %d", body, rec.Code)
		}
	}
	// Restart: only a running pool.
	if rec = e.do("POST", "/v1/php/app/restart", "", csrf, cookie); rec.Code != 200 {
		t.Errorf("restart: %d %s", rec.Code, rec.Body)
	}
	if rec = e.do("POST", "/v1/php/zzz/restart", "", csrf, cookie); rec.Code != 404 {
		t.Errorf("restart unknown: %d", rec.Code)
	}
	// A pool a site uses can't be deleted.
	web := strings.ReplaceAll(dir, `\`, "/") + "/web"
	if rec = e.do("POST", "/v1/site-files", `{"name":"p.test","template":"php","php_backend":"app","root":"`+web+`"}`, csrf, cookie); rec.Code != 200 {
		t.Fatalf("site: %d %s", rec.Code, rec.Body)
	}
	if rec = e.do("DELETE", "/v1/php-pools/app", "", csrf, cookie); rec.Code != 422 {
		t.Errorf("delete a pool in use: %d %s", rec.Code, rec.Body)
	}
	e.do("DELETE", "/v1/site-files/p.test", "", csrf, cookie)
	if rec = e.do("DELETE", "/v1/php-pools/app", "", csrf, cookie); rec.Code != 200 {
		t.Errorf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec = e.do("DELETE", "/v1/php-pools/app", "", csrf, cookie); rec.Code != 404 {
		t.Errorf("delete twice: %d", rec.Code)
	}
	if _, err := os.Stat(e.srv.opts.Sites.File(sitefile.ServerFile)); err == nil {
		t.Error("the empty server file was left")
	}
	// A viewer reads the list but changes nothing.
	vcookie, vcsrf := e.signIn("vic", "another long secret")
	if rec = e.do("GET", "/v1/php-pools", "", "", vcookie); rec.Code != 200 {
		t.Errorf("viewer list: %d", rec.Code)
	}
	if rec = e.do("PUT", "/v1/php-pools/x", `{"command":"php-cgi","processes":1}`, vcsrf, vcookie); rec.Code != 403 {
		t.Errorf("viewer save: %d", rec.Code)
	}
	if rec = e.do("POST", "/v1/php/app/restart", "", vcsrf, vcookie); rec.Code != 403 {
		t.Errorf("viewer restart: %d", rec.Code)
	}
}

func TestHTTPSWizard(t *testing.T) {
	e, _, cookie, csrf := siteEnv(t)
	body := `{"email":"ops@example.com","https":"0.0.0.0:443","http":"0.0.0.0:80","staging":true}`

	rec := e.do("GET", "/v1/https", "", "", cookie)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"acme":false`) || !strings.Contains(rec.Body.String(), `"spec":null`) {
		t.Fatalf("status before: %d %s", rec.Code, rec.Body)
	}
	// The server says the listener change needs a restart: the file is kept, and the answer says so.
	e.backend.reloadErr = control.NewError(control.CodeConfigInvalid, "the new configuration changes settings that need a restart; the running configuration was kept:\n  - the number of listeners changed (1 to 3)", nil)
	rec = e.do("PUT", "/v1/https", body, csrf, cookie)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"restart_required":true`) {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(e.srv.opts.Sites.File(sitefile.ServerFile)); err != nil {
		t.Errorf("the file should stay when only a restart is needed: %v", err)
	}
	rec = e.do("GET", "/v1/https", "", "", cookie)
	for _, want := range []string{`"acme":true`, `"acme_email":"ops@example.com"`, `"lotus-https"`, `"staging":true`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("status after: missing %s in %s", want, rec.Body)
		}
	}
	// Any other refusal undoes it.
	e.backend.reloadErr = control.NewError(control.CodeConfigInvalid, "address already in use", nil)
	if rec = e.do("PUT", "/v1/https", `{"email":"other@example.com","https":"0.0.0.0:8443"}`, csrf, cookie); rec.Code != 422 {
		t.Errorf("refused reload: %d %s", rec.Code, rec.Body)
	}
	text, _ := os.ReadFile(e.srv.opts.Sites.File(sitefile.ServerFile))
	if !strings.Contains(string(text), "ops@example.com") || strings.Contains(string(text), "other@example.com") {
		t.Errorf("the refused change was not undone:\n%s", text)
	}
	e.backend.reloadErr = nil
	// A bad value is refused.
	if rec = e.do("PUT", "/v1/https", `{"email":"nope","https":"0.0.0.0:443"}`, csrf, cookie); rec.Code != 422 {
		t.Errorf("bad email: %d", rec.Code)
	}
	// Removing it works, and a second removal says there is nothing.
	if rec = e.do("DELETE", "/v1/https", "", csrf, cookie); rec.Code != 200 {
		t.Errorf("remove: %d %s", rec.Code, rec.Body)
	}
	if rec = e.do("DELETE", "/v1/https", "", csrf, cookie); rec.Code != 404 {
		t.Errorf("remove twice: %d", rec.Code)
	}
	// Admin only.
	vcookie, _ := e.signIn("vic", "another long secret")
	if rec = e.do("GET", "/v1/https", "", "", vcookie); rec.Code != 403 {
		t.Errorf("viewer: %d", rec.Code)
	}
}

func TestDefaultPHPPoolIsChosenAndOffered(t *testing.T) {
	e, _, cookie, csrf := siteEnv(t)
	for _, n := range []string{"one", "two"} {
		if rec := e.do("PUT", "/v1/php-pools/"+n, `{"command":"php-cgi","processes":2,"timeout":60}`, csrf, cookie); rec.Code != 200 {
			t.Fatalf("add %s: %d %s", n, rec.Code, rec.Body)
		}
	}
	// The first pool is the default, and a new site form is told so.
	if rec := e.do("GET", "/v1/php-pools", "", "", cookie); !strings.Contains(rec.Body.String(), `"default":"one"`) {
		t.Errorf("first pool is the default: %s", rec.Body)
	}
	if rec := e.do("GET", "/v1/site-files", "", "", cookie); !strings.Contains(rec.Body.String(), `"default_php":"one"`) {
		t.Errorf("site form default: %s", rec.Body)
	}
	if rec := e.do("PUT", "/v1/php-default", `{"name":"two"}`, csrf, cookie); rec.Code != 200 {
		t.Fatalf("set default: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/v1/site-files", "", "", cookie); !strings.Contains(rec.Body.String(), `"default_php":"two"`) {
		t.Errorf("after change: %s", rec.Body)
	}
	if rec := e.do("PUT", "/v1/php-default", `{"name":"zzz"}`, csrf, cookie); rec.Code != 404 {
		t.Errorf("unknown pool: %d", rec.Code)
	}
	// Deleting the default hands the role to the first pool left.
	if rec := e.do("DELETE", "/v1/php-pools/two", "", "", cookie); rec.Code == 200 {
		t.Log("delete without csrf should not work")
	}
	if rec := e.do("DELETE", "/v1/php-pools/two", "", csrf, cookie); rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/v1/php-pools", "", "", cookie); !strings.Contains(rec.Body.String(), `"default":"one"`) {
		t.Errorf("after delete: %s", rec.Body)
	}
}
