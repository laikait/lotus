package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUsersPageRules(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.signIn("ada", "correct horse battery") // the only admin; vic is a viewer

	rec := e.do("GET", "/v1/users", "", "", cookie)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"vic"`) || strings.Contains(rec.Body.String(), "argon2") {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	// A viewer sees nothing of it.
	vcookie, vcsrf := e.signIn("vic", "another long secret")
	if rec := e.do("GET", "/v1/users", "", "", vcookie); rec.Code != 403 {
		t.Errorf("viewer list: %d", rec.Code)
	}
	if rec := e.do("POST", "/v1/users", `{"name":"x","role":"admin","password":"long enough pw"}`, vcsrf, vcookie); rec.Code != 403 {
		t.Errorf("viewer add: %d", rec.Code)
	}

	// Add: a short password and a repeated name are refused.
	if rec := e.do("POST", "/v1/users", `{"name":"bob","role":"viewer","password":"short"}`, csrf, cookie); rec.Code != 422 {
		t.Errorf("short password: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("POST", "/v1/users", `{"name":"bob","role":"viewer","password":"bob has a long one"}`, csrf, cookie); rec.Code != 200 {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("POST", "/v1/users", `{"name":"bob","role":"admin","password":"another long one"}`, csrf, cookie); rec.Code != 409 {
		t.Errorf("duplicate: %d", rec.Code)
	}

	// The last admin can't be deleted, demoted, or changed by itself.
	if rec := e.do("DELETE", "/v1/users/ada", "", csrf, cookie); rec.Code != 422 {
		t.Errorf("delete self: %d", rec.Code)
	}
	if rec := e.do("PUT", "/v1/users/ada", `{"role":"viewer"}`, csrf, cookie); rec.Code != 422 {
		t.Errorf("demote self: %d", rec.Code)
	}
	// Promote bob, then ada is no longer the last admin, and bob can demote ada.
	if rec := e.do("PUT", "/v1/users/bob", `{"role":"admin"}`, csrf, cookie); rec.Code != 200 {
		t.Fatalf("promote: %d %s", rec.Code, rec.Body)
	}
	bcookie, bcsrf := e.signIn("bob", "bob has a long one")
	if rec := e.do("PUT", "/v1/users/ada", `{"role":"viewer"}`, bcsrf, bcookie); rec.Code != 200 {
		t.Fatalf("demote ada: %d %s", rec.Code, rec.Body)
	}
	// ada's session ended with the change, so her old cookie is refused.
	if rec := e.do("GET", "/v1/users", "", "", cookie); rec.Code != 401 {
		t.Errorf("ada's session after the change: %d", rec.Code)
	}
	// bob is now the last admin.
	if rec := e.do("DELETE", "/v1/users/bob", "", bcsrf, bcookie); rec.Code != 422 {
		t.Errorf("delete the last admin: %d", rec.Code)
	}
	if rec := e.do("PUT", "/v1/users/ada", `{"password":"a brand new password"}`, bcsrf, bcookie); rec.Code != 200 {
		t.Errorf("reset another account's password: %d %s", rec.Code, rec.Body)
	}
	if _, ok := e.srv.opts.Users.Verify("ada", "a brand new password"); !ok {
		t.Error("the new password does not work")
	}

	// A password change of your own account needs the current password, and keeps your session.
	if rec := e.do("PUT", "/v1/users/bob", `{"password":"bob has a newer one"}`, bcsrf, bcookie); rec.Code != 403 {
		t.Errorf("own password without the current one: %d", rec.Code)
	}
	if rec := e.do("PUT", "/v1/users/bob", `{"password":"bob has a newer one","current_password":"wrong wrong wrong"}`, bcsrf, bcookie); rec.Code != 403 {
		t.Errorf("own password with a wrong current one: %d", rec.Code)
	}
	if rec := e.do("PUT", "/v1/users/bob", `{"password":"bob has a newer one","current_password":"bob has a long one"}`, bcsrf, bcookie); rec.Code != 200 {
		t.Fatalf("own password: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/v1/users", "", "", bcookie); rec.Code != 200 {
		t.Errorf("your own session should stay: %d", rec.Code)
	}

	// Deleting an account ends its sessions.
	if rec := e.do("DELETE", "/v1/users/vic", "", bcsrf, bcookie); rec.Code != 200 {
		t.Fatalf("delete vic: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/v1/session", "", "", vcookie); rec.Code != 401 {
		t.Errorf("vic's session after deletion: %d", rec.Code)
	}
	if rec := e.do("DELETE", "/v1/users/vic", "", bcsrf, bcookie); rec.Code != 404 {
		t.Errorf("delete twice: %d", rec.Code)
	}
}

func TestConfigHistory(t *testing.T) {
	e, dir, cookie, csrf := siteEnv(t)
	web := filepath.ToSlash(filepath.Join(dir, "web"))

	// Adding keeps nothing (there was no earlier text); each later change keeps the text before it.
	e.do("POST", "/v1/site-files", `{"name":"h.test","root":"`+web+`"}`, csrf, cookie)
	for _, idx := range []string{`["a.html"]`, `["b.html"]`} {
		if rec := e.do("PUT", "/v1/site-files/h.test", `{"root":"`+web+`","index":`+idx+`}`, csrf, cookie); rec.Code != 200 {
			t.Fatalf("update: %d %s", rec.Code, rec.Body)
		}
	}
	rec := e.do("GET", "/v1/config/history?name=sites.d/h.test.conf", "", "", cookie)
	if rec.Code != 200 || strings.Count(rec.Body.String(), `"who":"ada"`) != 2 {
		t.Fatalf("history: %d %s", rec.Code, rec.Body)
	}
	id := between(rec.Body.String(), `"id":"`, `"`) // the newest: the text before the second update
	rec = e.do("GET", "/v1/config/version?name=sites.d/h.test.conf&id="+id, "", "", cookie)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "a.html") {
		t.Errorf("version: %d %s", rec.Code, rec.Body)
	}

	// Editing the main file keeps its earlier text too.
	main, _ := os.ReadFile(filepath.Join(dir, "main.conf"))
	body := `{"name":"main.conf","content":` + jsonString(string(main)+"\n# a note\n") + `}`
	if rec := e.do("PUT", "/v1/config/file", body, csrf, cookie); rec.Code != 200 {
		t.Fatalf("save main: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/v1/config/history?name=main.conf", "", "", cookie); !strings.Contains(rec.Body.String(), `"who":"ada"`) {
		t.Errorf("main history: %s", rec.Body)
	}

	// Only ids and names the program made are accepted.
	for _, q := range []string{"name=main.conf&id=../../main", "name=main.conf&id=20260101-000000-x", "name=../x&id=" + id, "name=sites.d/h.test.conf&id=" + id + ".conf"} {
		if rec := e.do("GET", "/v1/config/version?"+q, "", "", cookie); rec.Code != 404 {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
	// A viewer reads no history.
	vcookie, _ := e.signIn("vic", "another long secret")
	if rec := e.do("GET", "/v1/config/history?name=main.conf", "", "", vcookie); rec.Code != 403 {
		t.Errorf("viewer: %d", rec.Code)
	}
}

func between(s, a, b string) string {
	i := strings.Index(s, a)
	if i < 0 {
		return ""
	}
	s = s[i+len(a):]
	if j := strings.Index(s, b); j >= 0 {
		return s[:j]
	}
	return s
}
