package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/control"
)

type fakeBackend struct {
	reloads   int
	reloadErr *control.Error // when set, Reload refuses
	purged    []control.CachePurgeParams
	stops     int
	reopened  int
}

func (f *fakeBackend) Status() control.Status {
	return control.Status{Version: "test", Uptime: "1m", Sites: 2}
}
func (f *fakeBackend) Health() control.Health { return control.Health{Status: "ok"} }
func (f *fakeBackend) Sites() []control.Site  { return []control.Site{{Name: "example.com"}} }
func (f *fakeBackend) Certificates() []control.Certificate {
	return nil
}
func (f *fakeBackend) CacheStatus(string) ([]control.CacheZone, *control.Error) {
	return []control.CacheZone{{Zone: "images", Entries: 3, Hits: 10}}, nil
}
func (f *fakeBackend) CachePurge(p control.CachePurgeParams) (int, *control.Error) {
	f.purged = append(f.purged, p)
	if !p.All && p.Path == "" && p.Prefix == "" {
		return 0, control.NewError(control.CodeInvalidParams, "say what to purge", nil)
	}
	return 3, nil
}
func (f *fakeBackend) Reload(string) (control.ReloadResult, *control.Error) {
	f.reloads++
	if f.reloadErr != nil {
		return control.ReloadResult{}, f.reloadErr
	}
	return control.ReloadResult{Generation: 7}, nil
}
func (f *fakeBackend) Stop(bool, time.Duration)                               { f.stops++ }
func (f *fakeBackend) ReopenLogs() (int, *control.Error)                      { f.reopened++; return 2, nil }
func (f *fakeBackend) InspectConfig(string) (json.RawMessage, *control.Error) { return nil, nil }

type env struct {
	t       *testing.T
	srv     *Server
	backend *fakeBackend
	now     time.Time
	h       http.Handler
}

func newEnv(t *testing.T, allow ...netip.Prefix) *env {
	t.Helper()
	users := Users{Path: filepath.Join(t.TempDir(), "admin-users.txt")}
	if err := users.Set("ada", RoleAdmin, "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if err := users.Set("vic", RoleViewer, "another long secret"); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, backend: &fakeBackend{}, now: time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)}
	e.srv = New(Options{Backend: e.backend, Users: users, SessionTTL: time.Hour, Allow: allow, Now: func() time.Time { return e.now }})
	e.h = e.srv.Handler()
	return e
}

func (e *env) do(method, path, body, csrf string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.RemoteAddr = "192.0.2.10:5555"
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

// signIn returns the session cookie and the CSRF token.
func (e *env) signIn(name, password string) (*http.Cookie, string) {
	e.t.Helper()
	rec := e.do("POST", "/v1/login", `{"name":"`+name+`","password":"`+password+`"}`, "")
	if rec.Code != 200 {
		e.t.Fatalf("sign in: %d %s", rec.Code, rec.Body)
	}
	var v map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
				e.t.Errorf("cookie must be HttpOnly and SameSite=Strict: %+v", c)
			}
			return c, v["csrf"]
		}
	}
	e.t.Fatal("no session cookie")
	return nil, ""
}

func TestPasswordsAreHashedAndChecked(t *testing.T) {
	u := Users{Path: filepath.Join(t.TempDir(), "u.txt")}
	if err := u.Set("ada", RoleAdmin, "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(u.Path)
	if strings.Contains(string(raw), "correct horse") || !strings.Contains(string(raw), "$argon2id$") {
		t.Fatalf("file: %s", raw)
	}
	if _, ok := u.Verify("ada", "correct horse battery"); !ok {
		t.Error("right password refused")
	}
	if _, ok := u.Verify("ada", "wrong password!!"); ok {
		t.Error("wrong password accepted")
	}
	if _, ok := u.Verify("nobody", "correct horse battery"); ok {
		t.Error("unknown name accepted")
	}
	// A second Set replaces the account, it does not add one.
	if err := u.Set("ada", RoleViewer, "a different secret"); err != nil {
		t.Fatal(err)
	}
	list, _ := u.List()
	if len(list) != 1 || list[0].Role != RoleViewer {
		t.Errorf("list: %+v", list)
	}
	for _, bad := range []struct{ name, role, pw string }{{"../x", RoleAdmin, "long enough pw"}, {"ok", "root", "long enough pw"}, {"ok", RoleAdmin, "short"}} {
		if err := u.Set(bad.name, bad.role, bad.pw); err == nil {
			t.Errorf("Set(%q, %q, ...) worked", bad.name, bad.role)
		}
	}
}

func TestSignInShowsTheDashboardData(t *testing.T) {
	e := newEnv(t)
	if rec := e.do("GET", "/v1/status", "", ""); rec.Code != 401 {
		t.Fatalf("status without a session: %d", rec.Code)
	}
	cookie, _ := e.signIn("ada", "correct horse battery")
	rec := e.do("GET", "/v1/status", "", "", cookie)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"version":"test"`) {
		t.Fatalf("status: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/v1/sites", "", "", cookie); !strings.Contains(rec.Body.String(), "example.com") {
		t.Errorf("sites: %s", rec.Body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
}

func TestWrongPasswordAndLockout(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < maxFailures; i++ {
		if rec := e.do("POST", "/v1/login", `{"name":"ada","password":"nope nope nope"}`, ""); rec.Code != 401 {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	// Even the right password is refused while locked out.
	if rec := e.do("POST", "/v1/login", `{"name":"ada","password":"correct horse battery"}`, ""); rec.Code != 429 {
		t.Fatalf("locked out: %d", rec.Code)
	}
	e.now = e.now.Add(failureWindow + time.Second)
	if rec := e.do("POST", "/v1/login", `{"name":"ada","password":"correct horse battery"}`, ""); rec.Code != 200 {
		t.Fatalf("after the window: %d", rec.Code)
	}
}

func TestChangesNeedTheCSRFTokenAndTheAdminRole(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.signIn("ada", "correct horse battery")

	if rec := e.do("POST", "/v1/reload", "", "", cookie); rec.Code != 403 {
		t.Errorf("reload without the token: %d", rec.Code)
	}
	if rec := e.do("POST", "/v1/reload", "", "wrong", cookie); rec.Code != 403 {
		t.Errorf("reload with a wrong token: %d", rec.Code)
	}
	if e.backend.reloads != 0 {
		t.Fatal("reloaded without a valid token")
	}
	if rec := e.do("POST", "/v1/reload", "", csrf, cookie); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"generation":7`) {
		t.Errorf("reload: %d %s", rec.Code, rec.Body)
	}

	vcookie, vcsrf := e.signIn("vic", "another long secret")
	if rec := e.do("POST", "/v1/reload", "", vcsrf, vcookie); rec.Code != 403 {
		t.Errorf("a viewer reloaded: %d", rec.Code)
	}
	if rec := e.do("GET", "/v1/status", "", "", vcookie); rec.Code != 200 {
		t.Errorf("a viewer cannot read: %d", rec.Code)
	}
	if e.backend.reloads != 1 {
		t.Errorf("reloads = %d", e.backend.reloads)
	}
}

func TestSessionsExpireAndEndOnSignOut(t *testing.T) {
	e := newEnv(t)
	cookie, _ := e.signIn("ada", "correct horse battery")
	e.now = e.now.Add(2 * time.Hour)
	if rec := e.do("GET", "/v1/status", "", "", cookie); rec.Code != 401 {
		t.Errorf("an expired session still works: %d", rec.Code)
	}

	cookie, csrf := e.signIn("ada", "correct horse battery")
	if rec := e.do("POST", "/v1/logout", "", csrf, cookie); rec.Code != 200 {
		t.Fatalf("logout: %d", rec.Code)
	}
	if rec := e.do("GET", "/v1/status", "", "", cookie); rec.Code != 401 {
		t.Errorf("the session survived the sign-out: %d", rec.Code)
	}
}

func TestOnlyAllowedNetworksReachIt(t *testing.T) {
	e := newEnv(t, netip.MustParsePrefix("10.0.0.0/8"))
	if rec := e.do("GET", "/", "", ""); rec.Code != 403 { // 192.0.2.10 is outside
		t.Errorf("outside the list: %d", rec.Code)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.1.2.3:4000"
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("inside the list: %d", rec.Code)
	}
}

func TestThePageIsServedWithTheSecurityHeaders(t *testing.T) {
	e := newEnv(t)
	rec := e.do("GET", "/", "", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "LOTUS") {
		t.Fatalf("index: %d", rec.Code)
	}
	for k, want := range map[string]string{"X-Frame-Options": "DENY", "X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer"} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q", k, got)
		}
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("CSP = %q", csp)
	}
	if strings.Contains(rec.Body.String(), "<script>") || strings.Contains(rec.Body.String(), " onclick=") {
		t.Error("the page has an inline script")
	}
	for _, p := range []string{"/app.js", "/app.css"} {
		if r := e.do("GET", p, "", ""); r.Code != 200 {
			t.Errorf("%s: %d", p, r.Code)
		}
	}
}
