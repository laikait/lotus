package config

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/laikait/lotus/internal/config/diag"
)

const acmeServer = `lotus_config_version 1
server {
    listen "http" { address "0.0.0.0:80" }
    listen "https" {
        address "0.0.0.0:443"
        protocol https
    }
    tls { acme {
%s
    } }
    site "example.com" {
        root "/srv/a"
%s
    }
}`

func TestCompileACMESettings(t *testing.T) {
	c := compile(t, strings.Replace(strings.Replace(acmeServer, "%s", `        email "admin@example.com"
        ca "https://acme.test/dir"
        ca_root "/etc/lotus/ca.pem"
        storage "/srv/certs"
        http_port 8080
        tls_alpn_port 8443
        challenges [ "tls-alpn-01" ]`, 1), "%s", "", 1))
	a := c.TLS.ACME
	if a == nil || a.Email != "admin@example.com" || a.CA != "https://acme.test/dir" || a.HTTPPort != 8080 || a.TLSALPNPort != 8443 ||
		len(a.Challenges) != 1 || a.Challenges[0] != "tls-alpn-01" ||
		!strings.HasSuffix(filepath.ToSlash(a.Storage), "/srv/certs") || !strings.HasSuffix(filepath.ToSlash(a.CARoot), "/etc/lotus/ca.pem") {
		t.Errorf("%+v", a)
	}
	if c.Sites[0].TLS != "automatic" {
		t.Errorf("a site on an https listener should default to automatic TLS, got %q", c.Sites[0].TLS)
	}
}

func TestACMEStorageDefault(t *testing.T) {
	c := compile(t, strings.Replace(strings.Replace(acmeServer, "%s", `        email "admin@example.com"`, 1), "%s", "", 1))
	got := filepath.ToSlash(c.TLS.ACME.Storage)
	if runtime.GOOS == "windows" {
		if !strings.HasSuffix(got, "lotus/certs") {
			t.Errorf("storage = %q", got)
		}
	} else if got != "/var/lib/lotus/certs" {
		t.Errorf("storage = %q", got)
	}
}

func TestACMEErrors(t *testing.T) {
	for name, tc := range map[string]struct{ acme, site, code, text string }{
		"wildcard site": {`email "a@b.c"`, `aliases [ "*.example.com" ]`, diag.CodeInvalidValue, "DNS-01"},
		"bad challenge": {`challenges [ "dns-01" ]`, "", diag.CodeInvalidValue, ""},
		"bad port":      {`http_port 70000`, "", diag.CodeInvalidValue, ""},
		"no challenges": {"challenges []", "", diag.CodeInvalidValue, ""},
	} {
		_, diags := Check("main.lotus", optsFor(map[string]string{"main.lotus": portable(strings.Replace(strings.Replace(acmeServer, "%s", tc.acme, 1), "%s", tc.site, 1))}))
		out := format(diags)
		if len(errorsOf(diags)) == 0 || !strings.Contains(out, tc.code) || !strings.Contains(out, tc.text) {
			t.Errorf("%s: want %s %q in:\n%s", name, tc.code, tc.text, out)
		}
	}
	// Automatic TLS needs the acme block (existing rule, still holds).
	src := `lotus_config_version 1
server {
    listen "https" { address "0.0.0.0:443"
        protocol https }
    site "example.com" { root "/srv/a" }
}`
	_, diags := Check("main.lotus", optsFor(map[string]string{"main.lotus": portable(src)}))
	if !strings.Contains(format(diags), diag.CodeSiteNoACME) {
		t.Errorf("a site with automatic TLS and no acme block:\n%s", format(diags))
	}
}

func TestACMESettingsAreAdminOnly(t *testing.T) {
	files := map[string]string{
		"main.lotus":   portable(strings.Replace(strings.Replace(acmeServer, "%s", `email "a@b.c"`, 1), "%s", `include "tenant.lotus"`, 1)),
		"tenant.lotus": `tls manual`,
	}
	_, diags := Check("main.lotus", optsFor(files))
	if !strings.Contains(format(diags), diag.CodeTenantBoundary) {
		t.Errorf("a tenant file chose how its site gets a certificate:\n%s", format(diags))
	}
}
