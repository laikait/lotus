package admin

import (
	"strings"
	"testing"

	"github.com/laikait/lotus/internal/control"
)

func TestRenewACertificate(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.signIn("ada", "correct horse battery")

	if rec := e.do("POST", "/v1/certs/renew", `{"domain":"example.com"}`, csrf, cookie); rec.Code != 200 {
		t.Fatalf("renew: %d %s", rec.Code, rec.Body)
	}
	if len(e.backend.renewed) != 1 || e.backend.renewed[0] != "example.com" {
		t.Errorf("the backend got %v", e.backend.renewed)
	}
	if rec := e.do("POST", "/v1/certs/renew", `{}`, csrf, cookie); rec.Code != 422 {
		t.Errorf("no domain: %d", rec.Code)
	}

	// The reason a renewal was refused reaches the page: an unknown name is 404, a recent one 422.
	e.backend.renewErr = control.NewError(control.CodeNotFound, "this name has no automatic certificate here", nil)
	if rec := e.do("POST", "/v1/certs/renew", `{"domain":"nope.test"}`, csrf, cookie); rec.Code != 404 || !strings.Contains(rec.Body.String(), "no automatic certificate") {
		t.Errorf("unknown: %d %s", rec.Code, rec.Body)
	}
	e.backend.renewErr = control.NewError(control.CodeInvalidParams, "issued less than a day ago", nil)
	if rec := e.do("POST", "/v1/certs/renew", `{"domain":"example.com"}`, csrf, cookie); rec.Code != 422 || !strings.Contains(rec.Body.String(), "less than a day") {
		t.Errorf("recent: %d %s", rec.Code, rec.Body)
	}
}

func TestOnlyAnAdminRenewsACertificate(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.signIn("vic", "another long secret")
	if rec := e.do("POST", "/v1/certs/renew", `{"domain":"example.com"}`, csrf, cookie); rec.Code != 403 {
		t.Errorf("a viewer renewed: %d", rec.Code)
	}
	acookie, _ := e.signIn("ada", "correct horse battery")
	if rec := e.do("POST", "/v1/certs/renew", `{"domain":"example.com"}`, "", acookie); rec.Code != 403 {
		t.Errorf("no CSRF token: %d", rec.Code)
	}
	if len(e.backend.renewed) != 0 {
		t.Error("a refused request renewed a certificate")
	}
}
