package server

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/fastcgi"
	"github.com/laikait/lotus/internal/fastcgi/fcgitest"
	"github.com/laikait/lotus/internal/htaccess"
	"github.com/laikait/lotus/internal/phpproc"
)

// passwords written by `openssl passwd -apr1`, PHP's password_hash and `htpasswd -s`:
// alice and bob use "secret", carol uses "myPassword".
const htpasswdFile = "# staff\n" +
	"alice:{SHA}5en6G6MezRroT3XKqkdPOmY/BfQ=\n" +
	"bob:$2y$04$enFGCfVtuR9MFSVSZradKuMNRQ3r3odf898vtkENCRYfyx5UFLFTC\n" +
	"carol:$apr1$r31.....$HqJZimcKQFAMYayBlzkrA/\n"

func basic(user, pass string) map[string]string {
	return map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))}
}

func TestHtaccessBasicAuthentication(t *testing.T) {
	_, base, _ := htServer(t, map[string]string{
		"private/.htaccess": "AuthType Basic\nAuthName \"Staff \\\"only\\\"\"\nAuthUserFile .htpasswd\nRequire valid-user\n",
		"private/.htpasswd": htpasswdFile,
		"private/a.txt":     "private text",
		"admin/.htaccess":   "AuthType Basic\nAuthName Admin\nAuthUserFile ../private/.htpasswd\nRequire user bob\n",
		"admin/a.txt":       "admin text",
	}, htConfig(), nil)

	resp, body := do(t, "GET", base+"/private/a.txt", nil, "")
	if resp.StatusCode != 401 || strings.Contains(body, "private text") {
		t.Fatalf("no credentials: %d %q", resp.StatusCode, body)
	}
	if got := resp.Header.Get("WWW-Authenticate"); got != `Basic realm="Staff \"only\"", charset="UTF-8"` {
		t.Errorf("WWW-Authenticate = %q", got)
	}
	for user, pass := range map[string]string{"alice": "secret", "bob": "secret", "carol": "myPassword"} {
		if resp, body := do(t, "GET", base+"/private/a.txt", basic(user, pass), ""); resp.StatusCode != 200 || body != "private text" {
			t.Errorf("%s: %d %q", user, resp.StatusCode, body)
		}
		if resp, _ := do(t, "GET", base+"/private/a.txt", basic(user, pass+"x"), ""); resp.StatusCode != 401 {
			t.Errorf("%s with a wrong password: %d", user, resp.StatusCode)
		}
	}
	for name, h := range map[string]map[string]string{
		"unknown user": basic("mallory", "secret"), "empty password": basic("alice", ""),
		"bearer": {"Authorization": "Bearer abc"}, "garbage": {"Authorization": "Basic !!!"},
	} {
		if resp, _ := do(t, "GET", base+"/private/a.txt", h, ""); resp.StatusCode != 401 {
			t.Errorf("%s: %d", name, resp.StatusCode)
		}
	}
	// The password file itself is never served, with or without credentials.
	if resp, body := do(t, "GET", base+"/private/.htpasswd", basic("alice", "secret"), ""); resp.StatusCode != 403 || strings.Contains(body, "alice") {
		t.Errorf("/private/.htpasswd: %d %q", resp.StatusCode, body)
	}
	// A rule for one user: a valid password of someone else is a 403, not a new prompt.
	if resp, _ := do(t, "GET", base+"/admin/a.txt", basic("bob", "secret"), ""); resp.StatusCode != 200 {
		t.Errorf("listed user: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", base+"/admin/a.txt", basic("alice", "secret"), ""); resp.StatusCode != 403 {
		t.Errorf("valid but not listed: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", base+"/admin/a.txt", nil, ""); resp.StatusCode != 401 || resp.Header.Get("WWW-Authenticate") == "" {
		t.Errorf("no credentials: %d", resp.StatusCode)
	}
	// Outside the protected directory nothing changes.
	if resp, _ := do(t, "GET", base+"/hello.txt", nil, ""); resp.StatusCode != 200 {
		t.Errorf("/hello.txt = %d", resp.StatusCode)
	}
}

func TestHtaccessBasicAuthUsesCustomErrorPageAndKeepsTheChallenge(t *testing.T) {
	_, base, _ := htServer(t, map[string]string{
		".htaccess": "ErrorDocument 401 \"please log in\"\nAuthType Basic\nAuthName x\nAuthUserFile .htpasswd\nRequire valid-user\n",
		".htpasswd": htpasswdFile,
	}, htConfig(), nil)
	resp, body := do(t, "GET", base+"/hello.txt", nil, "")
	if resp.StatusCode != 401 || body != "please log in" || resp.Header.Get("WWW-Authenticate") == "" {
		t.Errorf("%d %q %q", resp.StatusCode, body, resp.Header.Get("WWW-Authenticate"))
	}
}

func TestHtaccessPasswordFileMustBeInsideTheSite(t *testing.T) {
	outside := t.TempDir()
	must(t, os.WriteFile(filepath.Join(outside, "pw"), []byte(htpasswdFile), 0o644))
	for name, ref := range map[string]string{
		"absolute": filepath.ToSlash(filepath.Join(outside, "pw")),
		"relative": "../../../../../../../../../../../.." + filepath.ToSlash(filepath.Join(outside, "pw")),
	} {
		_, base, _ := htServer(t, map[string]string{
			".htaccess": "AuthType Basic\nAuthName x\nAuthUserFile \"" + ref + "\"\nRequire valid-user\n",
		}, htConfig(), nil)
		if resp, _ := do(t, "GET", base+"/hello.txt", basic("alice", "secret"), ""); resp.StatusCode != 401 {
			t.Errorf("%s: a password file outside the document root was used: %d", name, resp.StatusCode)
		}
	}
	// An absolute path inside the root works.
	_, base, root := htServer(t, map[string]string{".htpasswd": htpasswdFile}, htConfig(), nil)
	must(t, os.WriteFile(filepath.Join(root, ".htaccess"), []byte("AuthType Basic\nAuthName x\nAuthUserFile \""+filepath.ToSlash(filepath.Join(root, ".htpasswd"))+"\"\nRequire valid-user\n"), 0o644))
	if resp, _ := do(t, "GET", base+"/hello.txt", basic("alice", "secret"), ""); resp.StatusCode != 200 {
		t.Errorf("absolute path inside the root: %d", resp.StatusCode)
	}
	// A missing file locks everyone out; it never opens the directory.
	must(t, os.WriteFile(filepath.Join(root, ".htaccess"), []byte("AuthType Basic\nAuthName x\nAuthUserFile nothing\nRequire valid-user\n"), 0o644))
	if resp, _ := do(t, "GET", base+"/hello.txt", basic("alice", "secret"), ""); resp.StatusCode != 401 {
		t.Errorf("missing password file: %d", resp.StatusCode)
	}
}

func TestHtaccessPasswordFileChangesAreSeen(t *testing.T) {
	_, base, root := htServer(t, map[string]string{
		".htaccess":    "AuthType Basic\nAuthName x\nAuthUserFile pw/.htpasswd\nRequire valid-user\n",
		"pw/.htpasswd": "alice:{SHA}5en6G6MezRroT3XKqkdPOmY/BfQ=\n",
	}, htConfig(), nil)
	if resp, _ := do(t, "GET", base+"/hello.txt", basic("bob", "secret"), ""); resp.StatusCode != 401 {
		t.Fatalf("before: %d", resp.StatusCode)
	}
	p := filepath.Join(root, "pw", ".htpasswd")
	must(t, os.WriteFile(p, []byte(htpasswdFile), 0o644))
	bumpMtime(t, p)
	if resp, _ := do(t, "GET", base+"/hello.txt", basic("bob", "secret"), ""); resp.StatusCode != 200 {
		t.Errorf("a user added to the file can log in: %d", resp.StatusCode)
	}
	must(t, os.WriteFile(p, []byte("alice:{SHA}5en6G6MezRroT3XKqkdPOmY/BfQ=\n"), 0o644))
	bumpMtime(t, p)
	if resp, _ := do(t, "GET", base+"/hello.txt", basic("bob", "secret"), ""); resp.StatusCode != 401 {
		t.Errorf("a removed user can still log in: %d", resp.StatusCode)
	}
}

func TestHtaccessAuthMisconfigurationAnswers500(t *testing.T) {
	_, base, _ := htServer(t, map[string]string{
		".htaccess": "Require valid-user\n",
	}, htConfig(), nil)
	if resp, _ := do(t, "GET", base+"/hello.txt", basic("alice", "secret"), ""); resp.StatusCode != 500 {
		t.Errorf("%d", resp.StatusCode)
	}
}

func TestHtaccessBasicAuthWithPHPSetsREMOTE_USER(t *testing.T) {
	p := htPHP(t, map[string]string{
		".htaccess": "AuthType Basic\nAuthName x\nAuthUserFile .htpasswd\nRequire valid-user\n",
		".htpasswd": htpasswdFile,
	}, nil)
	if resp, _ := p.get(t, "/app.php"); resp.StatusCode != 401 {
		t.Fatalf("the script ran without credentials: %d", resp.StatusCode)
	}
	if got := p.calls.count(); got != 0 {
		t.Fatalf("the backend was called %d times for a refused request", got)
	}
	resp, _ := p.getWith(t, "/app.php", basic("carol", "myPassword"))
	if resp.StatusCode != 200 {
		t.Fatalf("with credentials: %d", resp.StatusCode)
	}
	params, _ := p.calls.last(t)
	if params["REMOTE_USER"] != "carol" || params["AUTH_TYPE"] != "Basic" {
		t.Errorf("REMOTE_USER %q AUTH_TYPE %q", params["REMOTE_USER"], params["AUTH_TYPE"])
	}
}

// ---- Expires, charset, types ----

func TestHtaccessExpiresCharsetAndTypes(t *testing.T) {
	_, base, _ := htServer(t, map[string]string{
		".htaccess": `AddType font/woff2 .woff2
AddDefaultCharset UTF-8
ExpiresActive On
ExpiresDefault "access plus 1 minute"
ExpiresByType font/woff2 "access plus 1 year"
ExpiresByType text/html "access plus 0 seconds"
`,
		"f.woff2": "font",
	}, htConfig(), nil)
	resp, _ := do(t, "GET", base+"/f.woff2", nil, "")
	if resp.Header.Get("Content-Type") != "font/woff2" || resp.Header.Get("Cache-Control") != "max-age=31536000" || resp.Header.Get("Expires") == "" {
		t.Errorf("woff2: %v", resp.Header)
	}
	resp, _ = do(t, "GET", base+"/page.html", nil, "")
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") || !strings.Contains(ct, "charset=") || resp.Header.Get("Cache-Control") != "max-age=0" {
		t.Errorf("html: %v", resp.Header)
	}
	resp, _ = do(t, "GET", base+"/hello.txt", nil, "")
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "charset=utf-8") || resp.Header.Get("Cache-Control") != "max-age=60" {
		t.Errorf("txt: %v", resp.Header)
	}
	if resp, _ := do(t, "GET", base+"/missing.txt", nil, ""); resp.StatusCode != 404 || resp.Header.Get("Cache-Control") != "" {
		t.Errorf("a 404 got a lifetime: %d %v", resp.StatusCode, resp.Header)
	}
}

func TestHtaccessExpiresDoesNotOverridePHPAndSecurityHeaders(t *testing.T) {
	p := htPHPWith(t, func(r *fcgitest.Req) {
		r.Respond("Content-Type: text/html\r\nCache-Control: no-store", "ok")
	}, map[string]string{
		".htaccess": "ExpiresActive On\nExpiresDefault \"access plus 1 year\"\nAddType text/plain .php\nAddDefaultCharset UTF-8\n",
	}, func(s *model.Site) {
		s.SecurityHeaders = []model.Header{{Name: "Cache-Control", Value: "no-cache"}}
	})
	resp, _ := p.get(t, "/app.php")
	if got := resp.Header.Values("Cache-Control"); len(got) != 1 || got[0] != "no-store" && got[0] != "no-cache" {
		t.Errorf("Cache-Control = %v", got)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/html; charset=UTF-8" {
		t.Errorf("AddType must not change a script's type, AddDefaultCharset may add a charset: %q", ct)
	}
}

// ---- php_value ----

func TestHtaccessPHPSettingsReachTheScript(t *testing.T) {
	p := htPHP(t, map[string]string{
		".htaccess":     "php_value memory_limit 256M\nphp_flag display_errors off\n",
		"sub/.htaccess": "php_value memory_limit 512M\nphp_value date.timezone Europe/Paris\n",
		"sub/a.php":     "x",
	}, nil)
	p.get(t, "/app.php")
	params, _ := p.calls.last(t)
	if got, want := params["PHP_VALUE"], "memory_limit='256M'\ndisplay_errors='Off'"; got != want {
		t.Errorf("PHP_VALUE = %q, want %q", got, want)
	}
	p.get(t, "/sub/a.php")
	params, _ = p.calls.last(t)
	if got, want := params["PHP_VALUE"], "memory_limit='512M'\ndisplay_errors='Off'\ndate.timezone='Europe/Paris'"; got != want {
		t.Errorf("PHP_VALUE in a subdirectory = %q, want %q", got, want)
	}
}

func TestHtaccessCannotSetPHPSettingsThroughEnvironmentVariables(t *testing.T) {
	p := htPHP(t, map[string]string{
		".htaccess": "RewriteEngine On\nRewriteRule .* - [E=PHP_VALUE:auto_prepend_file=/tmp/x,E=PHP_ADMIN_VALUE:disable_functions=,E=FCGI_ROLE:AUTHORIZER,E=php_value:y,E=MY_OK:1]\n",
	}, nil)
	p.get(t, "/app.php")
	params, _ := p.calls.last(t)
	for _, k := range []string{"PHP_VALUE", "PHP_ADMIN_VALUE", "FCGI_ROLE", "php_value"} {
		if v, ok := params[k]; ok {
			t.Errorf("%s = %q reached the backend", k, v)
		}
	}
	if params["MY_OK"] != "1" {
		t.Error("an ordinary variable was lost")
	}
}

func TestHtaccessPHPSettingsAreRefusedForManagedPools(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	set := []htaccess.PHPSetting{{Name: "memory_limit", Value: "1G"}}
	var managed *phpproc.Pool
	for mode, want := range map[string]bool{model.HTAccessError: false, model.HTAccessIgnore: true} {
		rt := &siteRuntime{log: log, ht: newHTRuntime(&model.HTAccess{OnUnsupported: mode}, log)}
		ini, ok := rt.phpSettings("b", managed, "/a.php", set)
		if ok != want || ini != "" {
			t.Errorf("mode %v: ini %q ok %v, want ok %v: php-cgi does not read PHP_VALUE, so it must not look applied", mode, ini, ok, want)
		}
	}
	rt := &siteRuntime{log: log, ht: newHTRuntime(htConfig(), log)}
	ini, ok := rt.phpSettings("b", &fakeBackend{}, "/a.php", set)
	if !ok || ini != "memory_limit='1G'" {
		t.Errorf("FPM backend: %q %v", ini, ok)
	}
	if _, ok := rt.phpSettings("b", &fakeBackend{}, "/a.php", []htaccess.PHPSetting{{Name: "a", Value: "it's"}}); ok {
		t.Error("a value with a quote was passed through")
	}
}

// fakeBackend stands for a PHP-FPM server in tests that never call it.
type fakeBackend struct{}

func (*fakeBackend) Do(context.Context, *fastcgi.Request) (*fastcgi.Response, error) {
	return nil, errors.New("not used")
}

func (p *phpTest) getWith(t *testing.T, path string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	return do(t, "GET", p.base+path, hdr, "")
}
