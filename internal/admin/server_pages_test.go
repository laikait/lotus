package admin

import (
	"strings"
	"testing"
)

func TestCacheStatusAndPurge(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.signIn("ada", "correct horse battery")

	rec := e.do("GET", "/v1/cache", "", "", cookie)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"zone":"images"`) {
		t.Fatalf("cache: %d %s", rec.Code, rec.Body)
	}

	rec = e.do("POST", "/v1/cache/purge", `{"zone":"images","prefix":"/blog/"}`, csrf, cookie)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"purged":3`) {
		t.Fatalf("purge: %d %s", rec.Code, rec.Body)
	}
	if len(e.backend.purged) != 1 || e.backend.purged[0].Prefix != "/blog/" || e.backend.purged[0].Zone != "images" {
		t.Errorf("purge params: %+v", e.backend.purged)
	}
	// Saying nothing to purge is refused by the backend and shown as such.
	if rec = e.do("POST", "/v1/cache/purge", `{"zone":"images"}`, csrf, cookie); rec.Code != 422 || !strings.Contains(rec.Body.String(), "say what to purge") {
		t.Errorf("empty purge: %d %s", rec.Code, rec.Body)
	}
}

func TestReopenLogs(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.signIn("ada", "correct horse battery")

	if rec := e.do("POST", "/v1/logs/reopen", "", csrf, cookie); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"reopened":2`) {
		t.Errorf("reopen: %d %s", rec.Code, rec.Body)
	}
}

// The admin cannot stop the server: there is no such route, even for an admin with a valid token.
func TestTheAdminHasNoStop(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.signIn("ada", "correct horse battery")
	if rec := e.do("POST", "/v1/stop", `{"confirm":true}`, csrf, cookie); rec.Code == 200 {
		t.Errorf("POST /v1/stop answered %d", rec.Code)
	}
	if e.backend.stops != 0 {
		t.Fatal("the server was stopped from the admin")
	}
}

func TestViewersCannotPurgeOrReopen(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.signIn("vic", "another long secret")
	for _, c := range [][2]string{{"/v1/cache/purge", `{"all":true}`}, {"/v1/logs/reopen", ``}} {
		if rec := e.do("POST", c[0], c[1], csrf, cookie); rec.Code != 403 {
			t.Errorf("viewer POST %s: %d", c[0], rec.Code)
		}
	}
	if rec := e.do("GET", "/v1/cache", "", "", cookie); rec.Code != 200 {
		t.Errorf("a viewer can't read the cache: %d", rec.Code)
	}
	if e.backend.stops != 0 || len(e.backend.purged) != 0 || e.backend.reopened != 0 {
		t.Error("a viewer changed something")
	}
}

func TestPHPStatus(t *testing.T) {
	e := newEnv(t)
	cookie, _ := e.signIn("ada", "correct horse battery")
	if rec := e.do("GET", "/v1/php", "", "", cookie); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("no pools: %d %s", rec.Code, rec.Body)
	}
	e.srv.opts.PHP = func() []PHPPool {
		return []PHPPool{{Name: "app", Command: "php-cgi", Processes: 4, Alive: 4, Restarts: 1}}
	}
	rec := e.do("GET", "/v1/php", "", "", cookie)
	if !strings.Contains(rec.Body.String(), `"alive":4`) || !strings.Contains(rec.Body.String(), `"name":"app"`) {
		t.Errorf("pools: %s", rec.Body)
	}
}

func TestTrafficIsReadableByAnyoneSignedIn(t *testing.T) {
	e := newEnv(t)
	if rec := e.do("GET", "/v1/traffic", "", ""); rec.Code != 401 {
		t.Errorf("without a session: %d", rec.Code)
	}
	cookie, _ := e.signIn("vic", "another long secret")
	if rec := e.do("GET", "/v1/traffic", "", "", cookie); rec.Code != 404 {
		t.Errorf("no traffic source: %d", rec.Code)
	}
	e.srv.opts.Traffic = func() Traffic {
		return Traffic{TimeMS: 1000, Classes: []string{"1xx", "2xx"}, Sites: []SiteTraffic{{Name: "a.test", Requests: 7, Class: []uint64{0, 7}}}}
	}
	rec := e.do("GET", "/v1/traffic", "", "", cookie)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"requests":7`) || !strings.Contains(rec.Body.String(), `"time_ms":1000`) {
		t.Errorf("traffic: %d %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
}
