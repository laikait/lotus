package config

import (
	"strings"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/config/diag"
	"github.com/laikait/lotus/internal/config/model"
)

const upstreamSrc = `
upstream "app" {
    backend "http://10.0.0.1:8080" { weight 3
        max_conns 50 }
    backend "https://api.internal"
    backend "unix:/run/app.sock"
    balance least_conn
    connect_timeout 2s
    response_timeout 30s
    retries 2
    max_fails 5
    fail_timeout 20s
    max_conns 500
    health_check { path "/healthz"
        interval 5s
        unhealthy_threshold 2 }
    tls_verify true
    tls_server_name "api.internal"
}
site "a" {
    root "/srv/a"
    route "/api/" {
        proxy "app"
        proxy_set_header "X-Env" "prod"
        proxy_set_header "X-Remove" ""
        proxy_host upstream
    }
    route "/static/" { try_files [ "{uri}", "=404" ] }
    route "/m/" { match { method [ "GET" ]
        proxy "app" } }
}`

func TestCompileUpstream(t *testing.T) {
	c := compile(t, cfg(upstreamSrc))
	if len(c.Upstreams) != 1 {
		t.Fatalf("%+v", c.Upstreams)
	}
	u := c.Upstreams[0]
	if u.Name != "app" || len(u.Backends) != 3 || u.Backends[0].Weight != 3 || u.Backends[0].MaxConns != 50 || u.Backends[1].Weight != 1 || !strings.HasPrefix(u.Backends[2].Address, "unix:") || !strings.HasSuffix(u.Backends[2].Address, "app.sock") {
		t.Errorf("backends: %+v", u.Backends)
	}
	if u.Balance != "least_conn" || time.Duration(u.ConnectTimeout) != 2*time.Second || time.Duration(u.ResponseTimeout) != 30*time.Second ||
		u.Retries != 2 || u.MaxFails != 5 || time.Duration(u.FailTimeout) != 20*time.Second || u.MaxConns != 500 || !u.TLSVerify || u.TLSServerName != "api.internal" {
		t.Errorf("settings: %+v", u)
	}
	if h := u.Health; h == nil || h.Path != "/healthz" || time.Duration(h.Interval) != 5*time.Second || h.UnhealthyThreshold != 2 || h.HealthyThreshold != 2 || time.Duration(h.Timeout) != 3*time.Second {
		t.Errorf("health: %+v", u.Health)
	}
	r := c.Sites[0].Routes[0]
	if r.Actions.Proxy != "app" || !r.Actions.ProxyHostUpstream || len(r.Actions.ProxyHeaders) != 2 || r.Actions.ProxyHeaders[1].Value != "" {
		t.Errorf("route: %+v", r.Actions)
	}
	if m := c.Sites[0].Routes[2].Matches; len(m) != 1 || m[0].Actions.Proxy != "app" {
		t.Errorf("match: %+v", m)
	}
}

func TestCompileUpstreamDefaults(t *testing.T) {
	c := compile(t, cfg(`upstream "x" { backend "http://127.0.0.1:1" }
site "a" { root "/srv/a" }`))
	u := c.Upstreams[0]
	if u.Balance != "round_robin" || !u.TLSVerify || u.Retries != model.DefaultUpstreamRetries || u.MaxFails != model.DefaultUpstreamMaxFails ||
		time.Duration(u.ConnectTimeout) != model.DefaultUpstreamConnectTimeout || u.Health != nil || u.MaxConns != 0 {
		t.Errorf("%+v", u)
	}
}

func TestUpstreamConfigurationErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		src  string
		code string
		hint string
	}{
		"unknown upstream": {`site "a" { root "/r"
 route "/" { proxy "nope" } }`, diag.CodeUnknownReference, "upstream"},
		"no backend":      {`upstream "x" { balance random }`, diag.CodeMissingRequired, "backend"},
		"bad scheme":      {`upstream "x" { backend "ftp://h" }`, diag.CodeInvalidValue, "backend"},
		"path in address": {`upstream "x" { backend "http://h:80/api" }`, diag.CodeInvalidValue, "rewrite"},
		"credentials":     {`upstream "x" { backend "http://u:p@h" }`, diag.CodeInvalidValue, ""},
		"empty unix":      {`upstream "x" { backend "unix:" }`, diag.CodeInvalidValue, ""},
		"bad balance": {`upstream "x" { backend "http://h"
 balance fastest }`, diag.CodeInvalidValue, ""},
		"weight zero": {`upstream "x" { backend "http://h" { weight 0 } }`, diag.CodeInvalidValue, ""},
		"cert without key": {`upstream "x" { backend "https://h"
 tls_client_cert "/c.pem" }`, diag.CodeMissingRequired, "both"},
		"proxy and files": {`upstream "x" { backend "http://h" }` + "\n" + `site "a" { root "/r"
 route "/" { proxy "x"
 try_files [ "{uri}" ] } }`, diag.CodeInvalidValue, "separate route"},
		"proxy and php": {`upstream "x" { backend "http://h" }` + "\n" + `site "a" { root "/r"
 route "/" { proxy "x"
 php "p" } }`, diag.CodeInvalidValue, ""},
		"forwarded header": {`upstream "x" { backend "http://h" }` + "\n" + `site "a" { root "/r"
 route "/" { proxy "x"
 proxy_set_header "X-Forwarded-For" "1.2.3.4" } }`, diag.CodeInvalidValue, "set by LOTUS"},
		"bad header name": {`upstream "x" { backend "http://h" }` + "\n" + `site "a" { root "/r"
 route "/" { proxy "x"
 proxy_set_header "Bad Name" "v" } }`, diag.CodeInvalidValue, ""},
		"duplicate backend": {`upstream "x" { backend "http://h:1"
 backend "http://h:1" }`, "", ""},
	} {
		_, diags := Check("main.conf", optsFor(map[string]string{"main.conf": portable(cfg(tc.src))}))
		errs := errorsOf(diags)
		if len(errs) == 0 {
			t.Errorf("%s: accepted", name)
			continue
		}
		if tc.code != "" {
			found := false
			for _, e := range errs {
				found = found || e.Code == tc.code
			}
			if !found {
				t.Errorf("%s: no %s in\n%s", name, tc.code, format(diags))
			}
		}
		if tc.hint != "" && !strings.Contains(format(diags), tc.hint) {
			t.Errorf("%s: the message lacks %q:\n%s", name, tc.hint, format(diags))
		}
	}
}

func TestTenantCannotChooseAnUpstream(t *testing.T) {
	files := map[string]string{
		"main.conf": portable(cfg(`upstream "x" { backend "http://h" }
site "a" { root "/r"
  include "tenant.conf" }`)),
		"tenant.conf": `route "/" { proxy "x" }`,
	}
	_, diags := Check("main.conf", optsFor(files))
	if !strings.Contains(format(diags), diag.CodeTenantBoundary) {
		t.Errorf("a tenant file chose an upstream:\n%s", format(diags))
	}
}
