package config

import (
	"strings"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/config/diag"
	"github.com/laikait/lotus/internal/config/model"
)

func TestCompileRateLimits(t *testing.T) {
	c := compile(t, `lotus_config_version 1
server {
    listen "http" { address "0.0.0.0:80" }
    limits { max_connections_per_ip 20 }
    site "a" {
        root "/srv/a"
        htaccess true
        htaccess_auth_failures 4
        htaccess_auth_lockout 10m
        rate_limit { requests 100
            per 1m
            burst 20 }
        route "/login" {
            rate_limit { requests 5
                per 10s
                key global }
        }
        route "/" { }
    }
}`)
	if c.Limits.MaxConnectionsPerIP != 20 {
		t.Errorf("limits: %+v", c.Limits)
	}
	s := c.Sites[0]
	if rl := s.RateLimit; rl == nil || rl.Requests != 100 || time.Duration(rl.Per) != time.Minute || rl.Burst != 20 || rl.Key != model.RateKeyIP {
		t.Errorf("site: %+v", s.RateLimit)
	}
	if rl := s.Routes[0].RateLimit; rl == nil || rl.Requests != 5 || time.Duration(rl.Per) != 10*time.Second || rl.Burst != 5 || rl.Key != model.RateKeyGlobal {
		t.Errorf("route: %+v", s.Routes[0].RateLimit)
	}
	if s.Routes[1].RateLimit != nil {
		t.Error("a route without rate_limit inherited the site's: the site's applies on its own")
	}
	if h := s.HTAccess; h == nil || h.AuthFailures != 4 || time.Duration(h.AuthLockout) != 10*time.Minute {
		t.Errorf("htaccess: %+v", s.HTAccess)
	}
}

func TestRateLimitDefaults(t *testing.T) {
	c := compile(t, cfg(`site "a" { root "/srv/a"
 htaccess true }`))
	if h := c.Sites[0].HTAccess; h.AuthFailures != model.DefaultHTAccessAuthFailures || time.Duration(h.AuthLockout) != model.DefaultHTAccessAuthLockout {
		t.Errorf("lock-out defaults: %+v", h)
	}
	if c.Sites[0].RateLimit != nil || c.Limits.MaxConnectionsPerIP != 0 {
		t.Error("limits must be off unless configured")
	}
}

func TestRateLimitErrors(t *testing.T) {
	for name, tc := range map[string]struct{ src, code, text string }{
		"no requests":  {`site "a" { rate_limit { per 1s } }`, diag.CodeMissingRequired, "requests"},
		"zero":         {`site "a" { rate_limit { requests 0 } }`, diag.CodeInvalidValue, ""},
		"bad key":      {`site "a" { rate_limit { requests 1` + "\n" + ` key header } }`, diag.CodeInvalidValue, ""},
		"tiny period":  {`site "a" { rate_limit { requests 1` + "\n" + ` per 0s } }`, diag.CodeInvalidValue, ""},
		"zero per ip":  {`limits { max_connections_per_ip 0 }`, diag.CodeInvalidValue, ""},
		"too many":     {`site "a" { htaccess true` + "\n" + ` htaccess_auth_failures 5000 }`, diag.CodeInvalidValue, ""},
		"zero lockout": {`site "a" { htaccess true` + "\n" + ` htaccess_auth_lockout 0s }`, diag.CodeInvalidValue, ""},
	} {
		_, diags := Check("main.conf", optsFor(map[string]string{"main.conf": portable(cfg(tc.src))}))
		errs := errorsOf(diags)
		if len(errs) == 0 {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !strings.Contains(format(diags), tc.code) || !strings.Contains(format(diags), tc.text) {
			t.Errorf("%s: expected %s %q in\n%s", name, tc.code, tc.text, format(diags))
		}
	}
}

func TestTenantCannotLoosenOrSetLimits(t *testing.T) {
	for _, inc := range []string{
		`rate_limit { requests 1000000 }`,
		`route "/" { rate_limit { requests 1000000 } }`,
		`htaccess_auth_failures 0`,
	} {
		files := map[string]string{
			"main.conf":   portable(cfg("site \"a\" {\n root \"/r\"\n include \"tenant.conf\"\n}")),
			"tenant.conf": inc,
		}
		_, diags := Check("main.conf", optsFor(files))
		if !strings.Contains(format(diags), diag.CodeTenantBoundary) {
			t.Errorf("a tenant file may set %q:\n%s", inc, format(diags))
		}
	}
}
