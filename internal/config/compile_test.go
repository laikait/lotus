package config

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/config/diag"
	"github.com/laikait/lotus/internal/config/loader"
	"github.com/laikait/lotus/internal/config/model"
)

func compile(t *testing.T, src string) *model.Config {
	t.Helper()
	res, diags := Check("main.lotus", optsFor(map[string]string{"main.lotus": portable(src)}))
	if res == nil {
		t.Fatalf("expected a valid configuration:\n%s", format(diags))
	}
	return res.Config
}

func TestCompileDefaults(t *testing.T) {
	cfg := compile(t, cfg(`site "a" { root "/srv/a" }`))
	if cfg.Version != 1 {
		t.Errorf("version = %d", cfg.Version)
	}
	if cfg.Limits != model.DefaultLimits() {
		t.Errorf("limits = %+v, want defaults %+v", cfg.Limits, model.DefaultLimits())
	}
	if cfg.TLS.MinVersion != "1.2" || cfg.TLS.ACME != nil {
		t.Errorf("tls = %+v", cfg.TLS)
	}
	if cfg.Runtime.MaxProcs != 0 || cfg.Runtime.MemoryLimit != 0 {
		t.Errorf("runtime = %+v", cfg.Runtime)
	}
	if len(cfg.Listeners) != 1 || cfg.Listeners[0] != (model.Listener{Name: "http", Address: "0.0.0.0:80"}) {
		t.Errorf("listeners = %+v", cfg.Listeners)
	}
	s := cfg.Sites[0]
	if s.Name != "a" || s.Root != abs("/srv/a") || len(s.Index) != 1 || s.Index[0] != "index.html" {
		t.Errorf("site = %+v", s)
	}
	if len(s.Listeners) != 1 || s.Listeners[0] != "http" {
		t.Errorf("a site with no listeners directive uses every listener; got %v", s.Listeners)
	}
	if s.TLS != model.TLSOff {
		t.Errorf("tls on a plain-HTTP listener should default to off, got %q", s.TLS)
	}
}

func TestCompileServerSettings(t *testing.T) {
	c := compile(t, `lotus_config_version 1
server {
    runtime { max_procs 4
 memory_limit 512mib }
    trusted_proxies [ "10.0.0.0/8", "192.0.2.7", "2001:db8::/32" ]
    limits { max_connections 50
 max_request_body 1mib
 idle_timeout 30s }
    listen "web" { address "[::]:443"
 protocol https
 http3 true }
    tls { min_version "1.3"
 acme { email "a@b.test"
 ca "https://ca.test/dir"
 eab_key_id "kid"
 eab_hmac_key "hmac" } }
    php_backend "fpm" { fastcgi_pass "unix:/run/php.sock" }
    php_backend "tcp" { fastcgi_pass "127.0.0.1:9000"
 timeout 5s }
    control { socket "/tmp/c.sock"
 viewer_group "ops" }
}
`)
	if c.Runtime.MaxProcs != 4 || c.Runtime.MemoryLimit != 512<<20 {
		t.Errorf("runtime = %+v", c.Runtime)
	}
	want := []string{"10.0.0.0/8", "192.0.2.7/32", "2001:db8::/32"}
	for i, p := range c.TrustedProxies {
		if p.String() != want[i] {
			t.Errorf("trusted proxy %d = %s, want %s", i, p, want[i])
		}
	}
	if c.Limits.MaxConnections != 50 || c.Limits.MaxRequestBody != 1<<20 || time.Duration(c.Limits.IdleTimeout) != 30*time.Second {
		t.Errorf("limits = %+v", c.Limits)
	}
	if c.Limits.HeaderTimeout != model.Duration(model.DefaultHeaderTimeout) {
		t.Error("settings that are not given keep their default")
	}
	if l := c.Listeners[0]; !l.HTTPS || !l.HTTP3 || l.Address != "[::]:443" {
		t.Errorf("listener = %+v", l)
	}
	if c.TLS.MinVersion != "1.3" || c.TLS.ACME == nil || c.TLS.ACME.CA != "https://ca.test/dir" || c.TLS.ACME.EABHMACKey.Reveal() != "hmac" {
		t.Errorf("tls = %+v", c.TLS)
	}
	if b := c.PHPBackends[0]; b.Network != "unix" || b.Address != abs("/run/php.sock") || time.Duration(b.Timeout) != model.DefaultPHPTimeout {
		t.Errorf("unix backend = %+v", b)
	}
	if b := c.PHPBackends[1]; b.Network != "tcp" || b.Address != "127.0.0.1:9000" || time.Duration(b.Timeout) != 5*time.Second {
		t.Errorf("tcp backend = %+v", b)
	}
	if c.Control.Socket != abs("/tmp/c.sock") || c.Control.ViewerGroup != "ops" {
		t.Errorf("control = %+v", c.Control)
	}
}

func TestCompileSiteInheritanceAndDefaults(t *testing.T) {
	c := compile(t, `lotus_config_version 1
server {
    limits { max_request_body 5mib
 read_timeout 10s }
    listen "http" { address "0.0.0.0:80" }
    listen "tls" { address "0.0.0.0:443"
 protocol https }
    tls { acme { email "a@b.test" } }
    site "Plain.Example.COM" {
        listeners [ "http" ]
        aliases [ "WWW.Plain.Example.com" ]
    }
    site "secure.example.com" { listeners [ "tls", "http" ] }
    site "manual.example.com" {
        listeners [ "tls" ]
        tls manual
        certificate "/etc/lotus/a.pem"
        certificate_key "/etc/lotus/a.key"
        limits { max_request_body 50mib }
        logs { access "/var/log/a.log"
 error null }
        security_headers { "X-Frame-Options" = "DENY" }
    }
}
`)
	plain, secure, manual := c.Sites[0], c.Sites[1], c.Sites[2]
	if plain.Name != "plain.example.com" || plain.Aliases[0] != "www.plain.example.com" {
		t.Errorf("hostnames must be lowercased: %q %v", plain.Name, plain.Aliases)
	}
	if plain.TLS != model.TLSOff {
		t.Errorf("plain.TLS = %q", plain.TLS)
	}
	if secure.TLS != model.TLSAutomatic {
		t.Errorf("a site on an https listener defaults to automatic TLS, got %q", secure.TLS)
	}
	if manual.TLS != model.TLSManual {
		t.Errorf("manual.TLS = %q", manual.TLS)
	}
	if plain.Limits.MaxRequestBody != 5<<20 || time.Duration(plain.Limits.ReadTimeout) != 10*time.Second {
		t.Errorf("sites inherit the server limits: %+v", plain.Limits)
	}
	if manual.Limits.MaxRequestBody != 50<<20 || time.Duration(manual.Limits.ReadTimeout) != 10*time.Second {
		t.Errorf("site limits override one field and inherit the rest: %+v", manual.Limits)
	}
	if manual.Logs.Access.Path != abs("/var/log/a.log") || manual.Logs.Access.Off || !manual.Logs.Error.Off {
		t.Errorf("logs = %+v", manual.Logs)
	}
	if len(manual.SecurityHeaders) != 1 || manual.SecurityHeaders[0] != (model.Header{Name: "X-Frame-Options", Value: "DENY"}) {
		t.Errorf("security headers = %+v", manual.SecurityHeaders)
	}
}

func TestCompileRoutes(t *testing.T) {
	c := compile(t, cfg(`
    php_backend "fpm" { fastcgi_pass "unix:/run/php.sock" }
    site "a" {
        root "/srv/a"
        index [ "index.php" ]
        limits { max_request_body 2mib }
        route "/" { try_files [ "{uri}", "/index.php?{query}" ] }
        route = "/ping" { return 200 "pong" }
        route ^~ "/static/" { index [ "index.html" ]
 max_request_body 100mib
            route "/static/big/" { root "/srv/big" }
        }
        route ~* '\.(png|jpg)$' { php_deny true }
        route ~ '\.php$' { php "fpm" }
        route "@fallback" { return 404 }
        route "/api/" {
            match {
                method [ "POST", "PUT" ]
                client_ip !~ [ "10.0.0.0/8" ]
                header "X-Bot" ~* 'bot'
                query "debug" "1"
                cookie "session" !~ '^[a-f0-9]+$'
                scheme https
                host [ "API.example.com" ]
                return 403
            }
        }
    }`))
	s := c.Sites[0]
	kinds := []model.RouteKind{model.RoutePrefix, model.RouteExact, model.RouteFinalPrefix, model.RouteRegexFold, model.RouteRegex, model.RoutePrefix}
	if len(s.Routes) != len(kinds) {
		t.Fatalf("got %d routes, want %d: %+v", len(s.Routes), len(kinds), s.Routes)
	}
	for i, k := range kinds {
		if s.Routes[i].Kind != k {
			t.Errorf("route %d kind = %s, want %s", i, s.Routes[i].Kind, k)
		}
	}
	if len(s.Named) != 1 || s.Named[0].Path != "@fallback" || s.Named[0].Actions.Return.Status != 404 {
		t.Errorf("named routes = %+v", s.Named)
	}

	root := s.Routes[0]
	if root.Root != abs("/srv/a") || root.Index[0] != "index.php" || root.MaxRequestBody != 2<<20 {
		t.Errorf("routes inherit site settings: %+v", root)
	}
	if got := root.Actions.TryFiles; len(got) != 2 || got[1] != "/index.php?{query}" {
		t.Errorf("try_files = %v", got)
	}
	if r := s.Routes[1].Actions.Return; r == nil || r.Status != 200 || r.Target != "pong" {
		t.Errorf("return = %+v", r)
	}

	static := s.Routes[2]
	if static.Index[0] != "index.html" || static.MaxRequestBody != 100<<20 || static.Root != abs("/srv/a") {
		t.Errorf("static = %+v", static)
	}
	if len(static.Children) != 1 {
		t.Fatalf("children = %+v", static.Children)
	}
	if ch := static.Children[0]; ch.Root != abs("/srv/big") || ch.MaxRequestBody != 100<<20 || ch.Index[0] != "index.html" {
		t.Errorf("a nested route inherits from its parent route: %+v", ch)
	}

	if re := s.Routes[3].Regexp; re == nil || !re.MatchString("/A.PNG") || !s.Routes[3].Actions.PHPDeny {
		t.Errorf("case-insensitive regex route = %+v", s.Routes[3])
	}
	if re := s.Routes[4].Regexp; re == nil || re.MatchString("/a.PHP") || !re.MatchString("/a.php") || s.Routes[4].Actions.PHP != "fpm" {
		t.Errorf("case-sensitive regex route = %+v", s.Routes[4])
	}

	m := s.Routes[5].Matches
	if len(m) != 1 {
		t.Fatalf("matches = %+v", m)
	}
	if got := strings.Join(m[0].Methods, ","); got != "POST,PUT" {
		t.Errorf("methods = %s", got)
	}
	if m[0].ClientIP == nil || !m[0].ClientIP.Not || m[0].ClientIP.Networks[0] != netip.MustParsePrefix("10.0.0.0/8") {
		t.Errorf("client_ip = %+v", m[0].ClientIP)
	}
	if h := m[0].Headers[0]; h.Name != "X-Bot" || h.Op != "~*" || h.Regexp == nil || !h.Regexp.MatchString("GoogleBOT") {
		t.Errorf("header pattern = %+v", h)
	}
	if q := m[0].Query[0]; q.Op != "=" || q.Value != "1" || q.Regexp != nil {
		t.Errorf("a plain value uses = and compiles no regexp: %+v", q)
	}
	if ck := m[0].Cookies[0]; ck.Op != "!~" || ck.Regexp == nil {
		t.Errorf("cookie pattern = %+v", ck)
	}
	if m[0].Scheme != "https" || m[0].Hosts[0] != "api.example.com" || m[0].Actions.Return.Status != 403 {
		t.Errorf("match = %+v", m[0])
	}
}

func TestCompileCrossChecks(t *testing.T) {
	tests := []struct {
		name string
		src  string
		code string
		at   string // line:col
		hint string
	}{
		{"duplicate hostname across sites", cfg(`site "a.test" { }
site "b.test" { aliases [ "A.test" ] }`), diag.CodeDuplicateHost, "5:27", "first claimed at"},
		{"site names that differ only by case", cfg(`site "Dup.test" { }
site "dup.test" { }`), diag.CodeDuplicateHost, "5:6", ""},
		{"same host twice in one site is harmless", "", "", "", ""},
		{"duplicate listener address", cfg(`listen "other" { address "0.0.0.0:80" }`), diag.CodeDuplicateAddress, "4:26", "first bound at"},
		{"automatic tls needs acme", cfg(`listen "s" { address "0.0.0.0:443"
 protocol https }
site "a.test" { listeners [ "s" ] }`), diag.CodeSiteNoACME, "6:6", "acme"},
		{"explicit automatic needs acme", cfg(`site "a.test" { tls automatic }`), diag.CodeSiteNoACME, "4:6", ""},
		{"site with no listener", "lotus_config_version 1\nserver {\n listen \"x\" { address \"0.0.0.0:80\" }\n site \"a.test\" { listeners [ \"x\" ] }\n}\n", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.src == "" {
				cfg := compile(t, cfg(`site "a.test" { aliases [ "a.test" ] }`))
				if len(cfg.Sites) != 1 {
					t.Error("expected one site")
				}
				return
			}
			res, diags := Check("main.lotus", optsFor(map[string]string{"main.lotus": tt.src}))
			if tt.code == "" {
				if res == nil {
					t.Fatalf("unexpected errors:\n%s", format(diags))
				}
				return
			}
			errs := errorsOf(diags)
			if res != nil || len(errs) != 1 || errs[0].Code != tt.code {
				t.Fatalf("want one %s error, got:\n%s", tt.code, format(diags))
			}
			if at := errs[0].Pos.String(); !strings.HasSuffix(at, ":"+tt.at) {
				t.Errorf("error at %s, want line:col %s", at, tt.at)
			}
			if tt.hint != "" && !strings.Contains(errs[0].Hint, tt.hint) {
				t.Errorf("hint = %q, want it to contain %q", errs[0].Hint, tt.hint)
			}
		})
	}
}

func TestCompileSkippedWhenValidationFails(t *testing.T) {
	// Validation errors stop the pipeline, so compile-only errors don't pile up
	// on top of them.
	_, diags := Check("main.lotus", optsFor(map[string]string{"main.lotus": cfg(`site "a.test" { tls automatic
 rot "x" }`)}))
	if got := codesOf(diags); len(got) != 1 || got[0] != diag.CodeUnknownDirective {
		t.Errorf("codes = %v\n%s", got, format(diags))
	}
}

func TestInspectJSONRedactsSecrets(t *testing.T) {
	res, diags := Check("main.lotus", optsFor(map[string]string{"main.lotus": portable(`lotus_config_version 1
let $t = env("SECRET")
server {
    listen "http" { address "0.0.0.0:80" }
    tls { acme { email env("LOTUS_ACME_EMAIL")
 eab_key_id "kid-1"
 eab_hmac_key "literal-hmac-value" } }
    site "a.test" {
        tls off
        root "/srv/a"
        route "/x/" { return 301 env("SECRET") }
        security_headers { "X-Token" = "Bearer ${t}" }
    }
}
`)}))
	if res == nil {
		t.Fatalf("diags:\n%s", format(diags))
	}
	out, err := res.InspectJSON("")
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, leaked := range []string{"hunter2", "ops@example.com", "literal-hmac-value"} {
		if strings.Contains(text, leaked) {
			t.Errorf("inspect output leaks %q:\n%s", leaked, text)
		}
	}
	if strings.Count(text, "<redacted>") < 3 {
		t.Errorf("expected redaction markers:\n%s", text)
	}
	// "Bearer ${t}" is built from a secret, so the whole value is secret.
	if strings.Contains(text, "Bearer") {
		t.Errorf("a value built from a secret must be redacted entirely:\n%s", text)
	}
	for _, kept := range []string{"kid-1", jsonEscaped(abs("/srv/a")), "a.test", `"value": "<redacted>"`} {
		if !strings.Contains(text, kept) {
			t.Errorf("inspect output should keep %q:\n%s", kept, text)
		}
	}
	var tree map[string]any
	if err := json.Unmarshal(out, &tree); err != nil {
		t.Fatalf("inspect output is not valid JSON: %v", err)
	}
	if _, ok := tree["sites"]; !ok {
		t.Errorf("missing sites in %v", tree)
	}
}

func TestSecretTypeNeverPrints(t *testing.T) {
	s := model.Secret("hunter2")
	for _, got := range []string{s.String(), s.GoString(), fmt.Sprintf("%v %+v %#v %s", s, s, s, s)} {
		if strings.Contains(got, "hunter2") {
			t.Errorf("leaked: %q", got)
		}
	}
	b, _ := json.Marshal(struct{ K model.Secret }{s})
	if strings.Contains(string(b), "hunter2") {
		t.Errorf("JSON leaked: %s", b)
	}
}

// ---- relative paths ----

var unixAbs = regexp.MustCompile(`"(unix:)?/(srv|run|tmp|var|etc)/`)

// portable makes the Unix-style absolute paths in a test configuration
// absolute on Windows too, by giving them a drive letter.
func portable(src string) string {
	if runtime.GOOS != "windows" {
		return src
	}
	return unixAbs.ReplaceAllString(src, `"${1}C:/${2}/`)
}

// abs returns the form of a Unix-style absolute path that the compiler produces.
func abs(p string) string {
	if runtime.GOOS == "windows" {
		p = "C:" + p
	}
	return filepath.Clean(filepath.FromSlash(p))
}

func TestRelativePathsAreResolvedAgainstTheRootFile(t *testing.T) {
	root, err := filepath.Abs("conf")
	if err != nil {
		t.Fatal(err)
	}
	want := func(rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }

	files := map[string]string{
		"conf/main.lotus": `lotus_config_version 1
server {
    listen "http" { address "0.0.0.0:80" }
    listen "tls" { address "0.0.0.0:443"
 protocol https }
    php_backend "p" { fastcgi_pass "unix:run/php.sock" }
    control { socket "run/lotus.sock" }
    site "a.test" {
        listeners [ "tls" ]
        root "./www/a"
        tls manual
        certificate "certs/../certs/a.pem"
        certificate_key "certs/a.key"
        logs { access "logs/a.log"
 error "logs/a.err" }
        route "/x/" { root "other" }
    }
    include "sites.d/extra.lotus"
}
`,
		// Paths in an included file are relative to the ROOT file, not to the include.
		"conf/sites.d/extra.lotus": `site "b.test" { listeners [ "http" ]
 root "from-include" }`,
	}
	res, diags := Check("conf/main.lotus", loader.Options{FS: loader.MemFS{Files: files}, Getenv: testEnv})
	if res == nil {
		t.Fatalf("diags:\n%s", format(diags))
	}
	c := res.Config
	s := c.Sites[0]
	checks := map[string][2]string{
		"site root":          {s.Root, want("www/a")},
		"certificate":        {s.Certificate, want("certs/a.pem")},
		"certificate key":    {s.CertificateKey, want("certs/a.key")},
		"access log":         {s.Logs.Access.Path, want("logs/a.log")},
		"error log":          {s.Logs.Error.Path, want("logs/a.err")},
		"route root":         {s.Routes[0].Root, want("other")},
		"included site root": {c.Sites[1].Root, want("from-include")},
		"php socket":         {c.PHPBackends[0].Address, want("run/php.sock")},
		"control socket":     {c.Control.Socket, want("run/lotus.sock")},
	}
	for name, got := range checks {
		if got[0] != got[1] {
			t.Errorf("%s = %q, want %q", name, got[0], got[1])
		}
		if !filepath.IsAbs(got[0]) {
			t.Errorf("%s = %q is not absolute; the result must not depend on the working directory", name, got[0])
		}
	}
}

func TestAbsolutePathsAreKept(t *testing.T) {
	dir := t.TempDir()
	rootPath := filepath.ToSlash(filepath.Join(dir, "site"))
	c := compile(t, cfg(`site "a.test" { root "`+rootPath+`" }`))
	if got := c.Sites[0].Root; got != filepath.Clean(filepath.Join(dir, "site")) {
		t.Errorf("root = %q", got)
	}
}

func TestSitesWithoutRootStayWithoutRoot(t *testing.T) {
	c := compile(t, cfg(`site "a.test" { }`))
	if c.Sites[0].Root != "" || c.Sites[0].Logs.Access.Path != "" {
		t.Errorf("unset paths must stay empty, not become the base directory: %+v", c.Sites[0])
	}
}

// jsonEscaped returns s as it appears inside a JSON string (backslashes doubled).
func jsonEscaped(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}
