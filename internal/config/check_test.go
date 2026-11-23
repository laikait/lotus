package config

import (
	"strings"
	"testing"

	"github.com/laikait/lotus/internal/config/diag"
	"github.com/laikait/lotus/internal/config/loader"
)

func testEnv(name string) (string, bool) {
	switch name {
	case "LOTUS_ACME_EMAIL":
		return "ops@example.com", true
	case "SECRET":
		return "hunter2", true
	case "BADRE":
		return "(hunter2", true // an invalid regular expression
	case "CTRL":
		return "hunter2\r\nSet-Cookie: x=1", true // control characters
	}
	return "", false
}

func optsFor(files map[string]string) loader.Options {
	return loader.Options{FS: loader.MemFS{Files: files}, Getenv: testEnv}
}

func run(files map[string]string) []diag.Diagnostic {
	_, diags := Check("main.lotus", optsFor(files))
	return diags
}

// cfg wraps body in a minimal valid server block. Body starts on line 4.
func cfg(body string) string {
	return "lotus_config_version 1\nserver {\n    listen \"http\" { address \"0.0.0.0:80\" }\n" + body + "\n}\n"
}

func errorsOf(diags []diag.Diagnostic) []diag.Diagnostic {
	var out []diag.Diagnostic
	for _, d := range diags {
		if d.Severity == diag.Error {
			out = append(out, d)
		}
	}
	return out
}

func codesOf(diags []diag.Diagnostic) []string {
	var out []string
	for _, d := range errorsOf(diags) {
		out = append(out, d.Code)
	}
	return out
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name  string
		src   string
		codes []string // expected error codes, in order
		hint  string   // substring expected in the first error's hint
		at    string   // expected position of the first error, "line:col"
	}{
		// ---- valid ----
		{name: "minimal site", src: cfg(`site "a" { root "/x" }`)},
		{name: "site with everything", src: cfg(`
    site "a" {
        listeners [ "http" ]
        aliases [ "www.a.test", "*.a.test" ]
        root "/var/www/a"
        index [ "index.php", "index.html" ]
        tls manual
        certificate "/etc/lotus/a.pem"
        certificate_key "/etc/lotus/a.key"
        limits { max_request_body 20mib; header_timeout 5s }
        security_headers {
            "X-Frame-Options" = "DENY"
        }
        route "/" { try_files [ "{uri}", "{uri}/", "/index.php?{query}" ] }
        route = "/health" { return 200 "ok" }
        route ^~ "/static/" { index [ "index.html" ] }
        route ~* '\.(png|jpg)$' { max_request_body 1mib }
        route "@fallback" { return 404 }
    }`)},
		{name: "env with default", src: cfg(`tls { acme { email env("NOPE", "me@example.com") } }`)},
		{name: "escaped braces are literal", src: cfg(`site "a" { root "/var/\{host\}" }`)},
		{name: "regex quantifier is not a placeholder", src: cfg(`site "a" { route ~ '^/a{2,3}$' { return 200 } }`)},

		// ---- unknown / wrong place ----
		{name: "typo in directive", src: cfg(`site "a" { rot "/x" }`), codes: []string{diag.CodeUnknownDirective}, hint: `"root"`, at: "4:12"},
		{name: "directive in wrong context", src: cfg(`site "a" { max_connections 5 }`), codes: []string{diag.CodeWrongContext}, hint: `"limits"`},
		{name: "site at top level", src: "lotus_config_version 1\nsite \"a\" { }\n", codes: []string{diag.CodeWrongContext, diag.CodeMissingRequired}, hint: `"server"`},
		{name: "entry in directive block", src: cfg(`site "a" { security_headers { foo 1 } }`), codes: []string{diag.CodeWrongContext}, hint: "entries"},

		// ---- cardinality ----
		{name: "single directive twice", src: cfg(`site "a" { root "/x"
 root "/y" }`), codes: []string{diag.CodeDuplicateDirective}, hint: "first set at"},
		{name: "duplicate site", src: cfg(`site "a" { }
site "a" { }`), codes: []string{diag.CodeDuplicateKey}},
		{name: "duplicate listener", src: cfg(`listen "http" { address "0.0.0.0:81" }`), codes: []string{diag.CodeDuplicateKey}},

		// ---- arguments ----
		{name: "missing argument", src: cfg(`site { }`), codes: []string{diag.CodeArgCount}, hint: "usage: site"},
		{name: "too many arguments", src: cfg(`site "a" "b" { }`), codes: []string{diag.CodeArgCount}},
		{name: "size without unit", src: cfg(`limits { max_request_body 10 }`), codes: []string{diag.CodeInvalidValue}, hint: "unit"},
		{name: "size as string", src: cfg(`limits { max_request_body "10mb" }`), codes: []string{diag.CodeInvalidValue}},
		{name: "duration without unit", src: cfg(`limits { header_timeout 15 }`), codes: []string{diag.CodeInvalidValue}, hint: "ms, s, m, h or d"},
		{name: "bad enum with suggestion", src: cfg(`listen "x" { address "0.0.0.0:81"
 protocol htps }`), codes: []string{diag.CodeInvalidValue}, hint: `"https"`},
		{name: "address without port", src: cfg(`listen "x" { address "0.0.0.0" }`), codes: []string{diag.CodeInvalidValue}},
		{name: "port out of range", src: cfg(`listen "x" { address "0.0.0.0:99999" }`), codes: []string{diag.CodeInvalidValue}},
		{name: "max_procs zero", src: cfg(`runtime { max_procs 0 }`), codes: []string{diag.CodeInvalidValue}},
		{name: "tls version", src: cfg(`tls { min_version "1.1" }`), codes: []string{diag.CodeInvalidValue}, hint: "1.2"},
		{name: "bad cidr in list points at the item", src: cfg(`trusted_proxies [ "10.0.0.0/8", "nonsense" ]`), codes: []string{diag.CodeInvalidValue}, at: "4:33"},
		{name: "status out of range", src: cfg(`site "a" { route "/" { return 99 } }`), codes: []string{diag.CodeInvalidValue}},

		// ---- blocks ----
		{name: "block on a scalar directive", src: cfg(`site "a" { root "/x" { } }`), codes: []string{diag.CodeBlockMismatch}},
		{name: "missing block", src: cfg(`site "a"`), codes: []string{diag.CodeBlockMismatch}},

		// ---- required ----
		{name: "server without listen", src: "lotus_config_version 1\nserver {\n}\n", codes: []string{diag.CodeMissingRequired}, at: "2:1"},
		{name: "listen without address", src: cfg(`listen "x" { protocol http }`), codes: []string{diag.CodeMissingRequired}},
		{name: "php backend without fastcgi_pass", src: cfg(`php_backend "p" { timeout 5s }`), codes: []string{diag.CodeMissingRequired}},

		// ---- cross references ----
		{name: "unknown listener", src: cfg(`site "a" { listeners [ "nope" ] }`), codes: []string{diag.CodeUnknownReference}, hint: "defined: http"},
		{name: "unknown php backend", src: cfg(`site "a" { route "/" { php "p83" } }`), codes: []string{diag.CodeUnknownReference}, hint: `php_backend "p83"`},
		{name: "known php backend", src: cfg(`php_backend "p83" { fastcgi_pass "unix:/run/php.sock" }
site "a" { route "/" { php "p83" } }`)},

		// ---- placeholders ----
		{name: "placeholder where not allowed", src: cfg(`site "a" { root "/var/{host}" }`), codes: []string{diag.CodePlaceholderNotAllowed}, hint: `\{`},
		{name: "unknown placeholder", src: cfg(`site "a" { route "/" { try_files [ "{urii}" ] } }`), codes: []string{diag.CodeUnknownPlaceholder}, hint: "{uri}"},
		{name: "keyed placeholder ok", src: cfg(`site "a" { route "/" { return 301 "/x/{query.page}/{header.User-Agent}" } }`)},

		// ---- routes ----
		{name: "invalid regex", src: cfg(`site "a" { route ~ '(a' { return 200 } }`), codes: []string{diag.CodeInvalidRegex}},
		{name: "route path without slash", src: cfg(`site "a" { route "x" { return 200 } }`), codes: []string{diag.CodeRoutePath}},
		{name: "route path with query", src: cfg(`site "a" { route "/x?y" { return 200 } }`), codes: []string{diag.CodeRoutePath}},
		{name: "bad named route", src: cfg(`site "a" { route "@Bad Name" { return 200 } }`), codes: []string{diag.CodeRoutePath}},
		{name: "route operator not allowed", src: cfg(`site "a" { route !~ "/x" { return 200 } }`), codes: []string{diag.CodeInvalidValue}},
		{name: "duplicate route", src: cfg(`site "a" {
 route "/a/" { return 200 }
 route "/a/" { return 200 }
}`), codes: []string{diag.CodeRouteDuplicate}},
		{name: "prefix and exact with same path are different", src: cfg(`site "a" {
 route "/a" { return 200 }
 route = "/a" { return 200 }
}`)},
		{name: "route outside parent prefix", src: cfg(`site "a" { route "/a/" { route "/b/" { return 200 } } }`), codes: []string{diag.CodeRouteNesting}},
		{name: "route inside exact route", src: cfg(`site "a" { route = "/a" { route "/a/x" { return 200 } } }`), codes: []string{diag.CodeRouteNesting}},
		{name: "named route nested", src: cfg(`site "a" { route "/a/" { route "@x" { return 200 } } }`), codes: []string{diag.CodeRouteNesting}},
		{name: "nested route inside parent prefix", src: cfg(`site "a" { route "/a/" { route "/a/b/" { return 200 }
 route ~ '\.php$' { return 200 } } }`)},

		// ---- match ----
		{name: "match without conditions", src: cfg(`site "a" { route "/" { match { return 403 } } }`), codes: []string{diag.CodeMissingRequired}, hint: "method"},
		{name: "match without action", src: cfg(`site "a" { route "/" { match { method [ "GET" ] } } }`), codes: []string{diag.CodeMissingRequired}, hint: "return"},
		{name: "valid match", src: cfg(`site "a" { route "/api/" { match { method [ "POST" ]
 client_ip !~ [ "10.0.0.0/8" ]
 header "X-Bot" ~* 'bot|spider'
 return 403 } } }`)},
		{name: "match header regex invalid", src: cfg(`site "a" { route "/" { match { header "X" ~ '(' ; return 403 } } }`), codes: []string{diag.CodeInvalidRegex}},

		// ---- combinations ----
		{name: "http3 needs https", src: cfg(`listen "q" { address "0.0.0.0:443"
 http3 true }`), codes: []string{diag.CodeInvalidCombination}, hint: "protocol https"},
		{name: "http3 with https is fine", src: cfg(`listen "q" { address "0.0.0.0:443"
 protocol https
 http3 true }`)},

		// ---- security_headers map ----
		{name: "bad header name", src: cfg(`site "a" { security_headers { "Bad Name" = "x" } }`), codes: []string{diag.CodeInvalidValue}},
		{name: "header injection", src: cfg(`site "a" { security_headers { "X-A" = "x\r\nSet-Cookie: a=b" } }`), codes: []string{diag.CodeInvalidValue}},
		{name: "header names are case-insensitive duplicates", src: cfg(`site "a" { security_headers {
 "X-A" = "1"
 "x-a" = "2"
} }`), codes: []string{diag.CodeDuplicateKey}},
		{name: "header value must be a string", src: cfg(`site "a" { security_headers { "X-A" = 5 } }`), codes: []string{diag.CodeInvalidValue}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diags := run(map[string]string{"main.lotus": tt.src})
			got := codesOf(diags)
			if strings.Join(got, ",") != strings.Join(tt.codes, ",") {
				t.Fatalf("error codes = %v, want %v\n%s", got, tt.codes, format(diags))
			}
			if len(got) == 0 {
				return
			}
			first := errorsOf(diags)[0]
			if tt.hint != "" && !strings.Contains(first.Hint, tt.hint) {
				t.Errorf("hint = %q, want it to contain %q", first.Hint, tt.hint)
			}
			if tt.at != "" {
				if at := first.Pos.String(); !strings.HasSuffix(at, ":"+tt.at) {
					t.Errorf("first error at %s, want line:col %s", at, tt.at)
				}
			}
		})
	}
}

func format(diags []diag.Diagnostic) string {
	var b strings.Builder
	for _, d := range diags {
		b.WriteString(d.Error() + "\n")
	}
	return b.String()
}

func TestSecretsNeverAppearInDiagnostics(t *testing.T) {
	// env("SECRET") is "hunter2". Using it where an integer is required must
	// produce an error that does not reveal the value.
	diags := run(map[string]string{"main.lotus": cfg(`limits { max_connections env("SECRET") }`)})
	if len(errorsOf(diags)) != 1 {
		t.Fatalf("diags:\n%s", format(diags))
	}
	if all := format(diags); strings.Contains(all, "hunter2") {
		t.Errorf("secret leaked into diagnostics:\n%s", all)
	}

	// Same through a variable and interpolation.
	diags = run(map[string]string{"main.lotus": "lotus_config_version 1\nlet $s = env(\"SECRET\")\nserver {\n listen \"h\" { address \"0.0.0.0:80\" }\n limits { max_request_body \"${s}-x\" }\n}\n"})
	if all := format(diags); strings.Contains(all, "hunter2") || len(errorsOf(diags)) == 0 {
		t.Errorf("expected an error without the secret:\n%s", all)
	}
}

func TestTenantBoundary(t *testing.T) {
	site := cfg(`site "a" {
    root "/srv/a"
    include "tenant.lotus"
}`)
	t.Run("safe directives are allowed", func(t *testing.T) {
		diags := run(map[string]string{
			"main.lotus":   site,
			"tenant.lotus": "index [ \"index.php\" ]\nroute \"/\" { return 200 }\nsecurity_headers { \"X-A\" = \"1\" }\n",
		})
		if got := codesOf(diags); len(got) != 0 {
			t.Fatalf("unexpected errors:\n%s", format(diags))
		}
	})
	forbidden := map[string]string{
		"root":    `root "/etc"`,
		"logs":    `logs { access "/etc/passwd" }`,
		"aliases": `aliases [ "victim.example" ]`,
		"limits":  `limits { max_request_body 100gib }`,
		"php":     `route "/" { php "other" }`,
		"tls":     `tls off`,
	}
	for name, body := range forbidden {
		t.Run("forbidden "+name, func(t *testing.T) {
			src := cfg(`php_backend "other" { fastcgi_pass "unix:/run/other.sock" }
` + `site "a" {
    include "tenant.lotus"
}`)
			diags := run(map[string]string{"main.lotus": src, "tenant.lotus": body})
			var found bool
			for _, d := range errorsOf(diags) {
				if d.Code == diag.CodeTenantBoundary {
					found = true
					if d.Pos.File != "tenant.lotus" {
						t.Errorf("reported in %q, want tenant.lotus", d.Pos.File)
					}
				}
			}
			if !found {
				t.Errorf("no %s error:\n%s", diag.CodeTenantBoundary, format(diags))
			}
		})
	}
	t.Run("a site defined in an included file may set its own directives", func(t *testing.T) {
		diags := run(map[string]string{
			"main.lotus":    cfg(`include "sites/a.lotus"`),
			"sites/a.lotus": "site \"a\" {\n root \"/srv/a\"\n limits { max_request_body 1mib }\n}\n",
		})
		if got := codesOf(diags); len(got) != 0 {
			t.Fatalf("unexpected errors:\n%s", format(diags))
		}
	})
}

func TestFullPipelineReturnsResult(t *testing.T) {
	res, diags := Check("main.lotus", loader.Options{
		FS:     loader.MemFS{Files: map[string]string{"main.lotus": cfg(`site "a" { root "/x" }`)}},
		Getenv: testEnv,
	})
	if res == nil || HasErrors(diags) {
		t.Fatalf("res=%v diags=%s", res, format(diags))
	}
	if res.Version != 1 || len(res.Files) != 1 {
		t.Errorf("result = %+v", res)
	}
	res, diags = Check("main.lotus", loader.Options{
		FS: loader.MemFS{Files: map[string]string{"main.lotus": cfg(`site "a" { rot "/x" }`)}},
	})
	if res != nil || !HasErrors(diags) {
		t.Error("invalid configuration must not produce a result")
	}
}

// Every place a diagnostic could echo a value must hide secrets. Each case
// uses env("SECRET") (value "hunter2") in a spot that fails validation.
func TestSecretsAreHiddenEverywhere(t *testing.T) {
	tests := map[string]string{
		"address": cfg(`listen "x" { address env("SECRET") }`),
		"enum": cfg(`listen "x" { address "0.0.0.0:81"
 protocol env("SECRET") }`),
		"cidr list item": cfg(`trusted_proxies [ "10.0.0.0/8", env("SECRET") ]`),
		"size":           cfg(`limits { max_request_body env("SECRET") }`),
		"path in route":  cfg(`site "a" { route env("SECRET") { return 200 } }`),
		"regex route":    cfg(`site "a" { route ~ env("BADRE") { return 200 } }`),
		"duplicate site key": cfg(`site "a" { }
site "a" { }
site env("SECRET") { }
site env("SECRET") { }`),
		"unknown listener":    cfg(`site "a" { listeners [ env("SECRET") ] }`),
		"unknown php backend": cfg(`site "a" { route "/" { php env("SECRET") } }`),
		"map key":             cfg(`site "a" { security_headers { env("SECRET") = "x" } }`),
		"map value": cfg(`site "a" { security_headers { "X-A" = env("CTRL")
 } }`),
		"duplicate map key": cfg(`site "a" { security_headers {
 env("SECRET") = "1"
 env("SECRET") = "2"
} }`),
		"header regex": cfg(`site "a" { route "/" { match { header "X" ~ env("BADRE")
 return 403 } } }`),
		"include path missing": "lotus_config_version 1\ninclude env(\"SECRET\")\n",
		"file path missing":    "lotus_config_version 1\nd file(env(\"SECRET\"))\n",
		"env name unset":       "lotus_config_version 1\nd env(env(\"SECRET\"))\n",
	}
	for name, src := range tests {
		t.Run(name, func(t *testing.T) {
			diags := run(map[string]string{"main.lotus": src})
			if len(errorsOf(diags)) == 0 {
				t.Fatalf("expected at least one error (the test must exercise a failing path)\n%s", format(diags))
			}
			if all := format(diags); strings.Contains(all, "hunter2") {
				t.Errorf("secret leaked:\n%s", all)
			}
		})
	}
}

func TestSiteTLSFilesAndPolicies(t *testing.T) {
	tests := []struct {
		name  string
		src   string
		codes []string
	}{
		{"manual with files", cfg(`site "a" { tls manual
 certificate "/c.pem"
 certificate_key "/k.pem" }`), nil},
		{"manual without certificate", cfg(`site "a" { tls manual
 certificate_key "/k.pem" }`), []string{diag.CodeMissingRequired}},
		{"manual without key", cfg(`site "a" { tls manual
 certificate "/c.pem" }`), []string{diag.CodeMissingRequired}},
		{"manual without either reports both", cfg(`site "a" { tls manual }`), []string{diag.CodeMissingRequired, diag.CodeMissingRequired}},
		{"files without manual", cfg(`site "a" { certificate "/c.pem"
 certificate_key "/k.pem" }`), []string{diag.CodeInvalidCombination, diag.CodeInvalidCombination}},
		{"policies", cfg(`site "a" { symlinks deny
 dotfiles allow }`), nil},
		{"bad symlink policy", cfg(`site "a" { symlinks maybe }`), []string{diag.CodeInvalidValue}},
		{"bad dotfile policy", cfg(`site "a" { dotfiles yes }`), []string{diag.CodeInvalidValue}},
		{"tls off on an https listener", cfg(`listen "s" { address "0.0.0.0:443"
 protocol https }
tls { acme { email "a@b.test" } }
site "a" { listeners [ "s" ]
 tls off }`), []string{diag.CodeTLSOffOnHTTPS}},
		{"tls off on an http listener", cfg(`site "a" { tls off }`), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := codesOf(run(map[string]string{"main.lotus": tt.src}))
			if strings.Join(got, ",") != strings.Join(tt.codes, ",") {
				t.Errorf("codes = %v, want %v", got, tt.codes)
			}
		})
	}
}

func TestSitePoliciesAreCompiled(t *testing.T) {
	c := compile(t, cfg(`site "default.test" { }
site "custom.test" { symlinks follow
 dotfiles allow }
site "tenant.test" { }`))
	if s := c.Sites[0]; s.Symlinks != "owner_match" || s.Dotfiles != "deny" {
		t.Errorf("defaults must be the safe ones: %+v", s)
	}
	if s := c.Sites[1]; s.Symlinks != "follow" || s.Dotfiles != "allow" {
		t.Errorf("custom = %+v", s)
	}
}

func TestTenantCannotChangeFilePolicies(t *testing.T) {
	for _, body := range []string{`symlinks follow`, `dotfiles allow`, `certificate "/x"`, `certificate_key "/x"`} {
		diags := run(map[string]string{
			"main.lotus":   cfg(`site "a" { include "tenant.lotus" }`),
			"tenant.lotus": body,
		})
		var found bool
		for _, d := range errorsOf(diags) {
			found = found || d.Code == diag.CodeTenantBoundary
		}
		if !found {
			t.Errorf("%q: expected %s\n%s", body, diag.CodeTenantBoundary, format(diags))
		}
	}
}
