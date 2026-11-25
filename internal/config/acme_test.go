package config

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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
		if !strings.HasSuffix(got, "Lotus/certs") {
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
		_, diags := Check("main.conf", optsFor(map[string]string{"main.conf": portable(strings.Replace(strings.Replace(acmeServer, "%s", tc.acme, 1), "%s", tc.site, 1))}))
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
	_, diags := Check("main.conf", optsFor(map[string]string{"main.conf": portable(src)}))
	if !strings.Contains(format(diags), diag.CodeSiteNoACME) {
		t.Errorf("a site with automatic TLS and no acme block:\n%s", format(diags))
	}
}

func TestACMESettingsAreAdminOnly(t *testing.T) {
	files := map[string]string{
		"main.conf":   portable(strings.Replace(strings.Replace(acmeServer, "%s", `email "a@b.c"`, 1), "%s", `include "tenant.conf"`, 1)),
		"tenant.conf": `tls manual`,
	}
	_, diags := Check("main.conf", optsFor(files))
	if !strings.Contains(format(diags), diag.CodeTenantBoundary) {
		t.Errorf("a tenant file chose how its site gets a certificate:\n%s", format(diags))
	}
}

func acmeWith(acme, site string) string {
	return portable(strings.Replace(strings.Replace(acmeServer, "%s", acme, 1), "%s", site, 1))
}

func TestCompileDNSSettings(t *testing.T) {
	c := compile(t, acmeWith(`email "a@b.c"
        dns {
            provider "cloudflare"
            api_token "tok-123"
            propagation_delay 5s
            propagation_timeout 90s
            propagation_check false
            resolvers [ "1.1.1.1:53", "8.8.8.8:53" ]
        }`, `aliases [ "*.example.com" ]`))
	d := c.TLS.ACME.DNS
	if d == nil || d.Provider != "cloudflare" || string(d.APIToken) != "tok-123" || time.Duration(d.PropagationDelay) != 5*time.Second ||
		time.Duration(d.PropagationTimeout) != 90*time.Second || !d.SkipPropagationCheck || len(d.Resolvers) != 2 {
		t.Errorf("%+v", d)
	}
	// The token never appears in a dump of the configuration.
	b, _ := json.Marshal(c)
	if strings.Contains(string(b), "tok-123") {
		t.Errorf("the API token is in the JSON form: %s", b)
	}
	// With a dns block the wildcard alias is accepted, and dns-01 is among the defaults.
	if len(c.TLS.ACME.Challenges) != 0 {
		t.Errorf("challenges: %v", c.TLS.ACME.Challenges)
	}

	e := compile(t, acmeWith(`dns {
            provider "exec"
            command "/usr/local/bin/lotus-dns"
        }
        challenges [ "dns-01" ]`, ""))
	if d := e.TLS.ACME.DNS; d == nil || d.Provider != "exec" || !strings.HasSuffix(filepath.ToSlash(d.Command), "/usr/local/bin/lotus-dns") || d.SkipPropagationCheck {
		t.Errorf("%+v", d)
	}
}

func TestDNSErrors(t *testing.T) {
	for name, tc := range map[string]struct{ acme, site, text string }{
		"dns-01 without a dns block":    {`challenges [ "dns-01" ]`, "", "no dns block"},
		"dns block without dns-01":      {"challenges [ \"http-01\" ]\n dns { provider \"exec\"\n command \"x\" }", "", "does not list dns-01"},
		"cloudflare without a token":    {`dns { provider "cloudflare" }`, "", "api_token"},
		"cloudflare with a command":     {"dns { provider \"cloudflare\"\n api_token \"t\"\n command \"x\" }", "", "exec dns provider"},
		"exec without a command":        {`dns { provider "exec" }`, "", "needs a command"},
		"exec with a token":             {"dns { provider \"exec\"\n command \"x\"\n api_token \"t\" }", "", "cloudflare dns provider"},
		"no provider":                   {`dns { propagation_delay 1s }`, "", "provider"},
		"unknown provider":              {`dns { provider "route66" }`, "", ""},
		"wildcard without dns":          {`email "a@b.c"`, `aliases [ "*.example.com" ]`, "DNS-01"},
		"wildcard when dns is excluded": {"challenges [ \"http-01\" ]", `aliases [ "*.example.com" ]`, "DNS-01"},
	} {
		_, diags := Check("main.conf", optsFor(map[string]string{"main.conf": acmeWith(tc.acme, tc.site)}))
		out := format(diags)
		if len(errorsOf(diags)) == 0 || !strings.Contains(out, tc.text) {
			t.Errorf("%s: want an error mentioning %q:\n%s", name, tc.text, out)
		}
	}
}

func TestDNSSettingsAreAdminOnly(t *testing.T) {
	files := map[string]string{
		"main.conf":   acmeWith(`email "a@b.c"`, `include "tenant.conf"`),
		"tenant.conf": "dns { provider \"exec\"\n command \"/bin/sh\" }",
	}
	_, diags := Check("main.conf", optsFor(files))
	if len(errorsOf(diags)) == 0 {
		t.Errorf("a tenant file configured DNS credentials or a command:\n%s", format(diags))
	}
}
