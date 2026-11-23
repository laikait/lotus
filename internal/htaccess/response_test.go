package htaccess

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"
)

func eval(t *testing.T, src, path string) *Decision {
	t.Helper()
	c := compileClean(t, src)
	return Evaluate(rootLayer(c), newReq(fakeFS{}, path, ""), false)
}

func TestParseLifetime(t *testing.T) {
	ok := map[string]time.Duration{
		"access plus 1 month":         30 * 24 * time.Hour,
		"access plus 1 year 2 days":   367 * 24 * time.Hour,
		"now plus 3 hours":            3 * time.Hour,
		"ACCESS PLUS 10 Minutes":      10 * time.Minute,
		"A3600":                       time.Hour,
		"access plus 1 week 1 second": 7*24*time.Hour + time.Second,
	}
	for in, want := range ok {
		if got, err := parseLifetime(in); err != nil || got != want {
			t.Errorf("%q = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "access", "access plus", "access plus 1", "access plus x days", "access plus 1 fortnight",
		"modification plus 1 day", "M3600", "access minus 1 day", "access plus -1 day", "access plus 99999 years", "A-5"} {
		if _, err := parseLifetime(in); err == nil {
			t.Errorf("%q was accepted", in)
		}
	}
}

func TestExpires(t *testing.T) {
	src := "ExpiresActive On\nExpiresDefault \"access plus 1 hour\"\nExpiresByType image/png \"access plus 1 year\"\nExpiresByType text/* \"access plus 5 minutes\""
	d := eval(t, src, "/a")
	if d.Policy == nil || d.Policy.Expires == nil {
		t.Fatal("no policy")
	}
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	apply := func(ct string, status int, pre map[string]string) http.Header {
		h := http.Header{}
		if ct != "" {
			h.Set("Content-Type", ct)
		}
		for k, v := range pre {
			h.Set(k, v)
		}
		d.Policy.Apply(h, "/a", status, false, now)
		return h
	}
	if h := apply("image/png", 200, nil); h.Get("Cache-Control") != "max-age=31536000" || h.Get("Expires") != now.Add(365*24*time.Hour).Format(http.TimeFormat) {
		t.Errorf("png: %v", h)
	}
	if h := apply("text/css; charset=utf-8", 200, nil); h.Get("Cache-Control") != "max-age=300" {
		t.Errorf("text/*: %v", h)
	}
	if h := apply("application/json", 200, nil); h.Get("Cache-Control") != "max-age=3600" {
		t.Errorf("default: %v", h)
	}
	if h := apply("image/png", 404, nil); h.Get("Cache-Control") != "" || h.Get("Expires") != "" {
		t.Errorf("an error response got a lifetime: %v", h)
	}
	if h := apply("image/png", 302, nil); h.Get("Cache-Control") != "" {
		t.Errorf("a redirect got a lifetime: %v", h)
	}
	if h := apply("image/png", 200, map[string]string{"Cache-Control": "no-store"}); h.Get("Cache-Control") != "no-store, max-age=31536000" {
		t.Errorf("existing Cache-Control: %v", h)
	}
	if h := apply("image/png", 200, map[string]string{"Cache-Control": "max-age=5"}); h.Get("Cache-Control") != "max-age=5" || h.Get("Expires") != "" {
		t.Errorf("a script's own max-age must win: %v", h)
	}
	if h := apply("image/png", 200, map[string]string{"Expires": "Thu, 01 Jan 1970 00:00:00 GMT"}); h.Get("Cache-Control") != "" {
		t.Errorf("a script's own Expires must win: %v", h)
	}
}

func TestExpiresNeedsActiveAndNestsDownwards(t *testing.T) {
	if d := eval(t, "ExpiresDefault \"access plus 1 hour\"", "/a"); d.Policy != nil {
		t.Error("ExpiresDefault without ExpiresActive On has an effect")
	}
	parent := compileClean(t, "ExpiresActive On\nExpiresDefault \"access plus 1 hour\"\nExpiresByType image/png \"access plus 1 day\"")
	child := compileClean(t, "ExpiresByType image/png \"access plus 1 week\"")
	d := Evaluate([]Layer{{DirURL: "/", File: parent}, {DirURL: "/a/", File: child}}, newReq(fakeFS{}, "/a/x", ""), false)
	if d.Policy.Expires.ByType["image/png"] != 7*24*time.Hour || d.Policy.Expires.Default != time.Hour {
		t.Errorf("%+v", d.Policy.Expires)
	}
	off := compileClean(t, "ExpiresActive Off")
	d = Evaluate([]Layer{{DirURL: "/", File: parent}, {DirURL: "/a/", File: off}}, newReq(fakeFS{}, "/a/x", ""), false)
	if d.Policy != nil {
		t.Error("ExpiresActive Off in a child did not turn it off")
	}
}

func TestCharsetAndTypes(t *testing.T) {
	d := eval(t, "AddDefaultCharset UTF-8\nAddType font/woff2 .woff2\nAddType image/avif avif", "/f.woff2")
	h := http.Header{"Content-Type": {"text/html"}}
	d.Policy.Apply(h, "/x.html", 200, false, time.Now())
	if h.Get("Content-Type") != "text/html; charset=UTF-8" {
		t.Error(h)
	}
	h = http.Header{"Content-Type": {"text/html; charset=latin1"}}
	d.Policy.Apply(h, "/x", 200, false, time.Now())
	if h.Get("Content-Type") != "text/html; charset=latin1" {
		t.Errorf("an existing charset was replaced: %v", h)
	}
	h = http.Header{"Content-Type": {"application/json"}}
	d.Policy.Apply(h, "/x", 200, false, time.Now())
	if h.Get("Content-Type") != "application/json" {
		t.Errorf("charset added to a non-text type: %v", h)
	}
	for path, want := range map[string]string{"/a/F.WOFF2": "font/woff2", "/x.avif": "image/avif"} {
		h = http.Header{"Content-Type": {"application/octet-stream"}}
		d.Policy.Apply(h, path, 200, false, time.Now())
		if h.Get("Content-Type") != want {
			t.Errorf("%s: %v", path, h)
		}
	}
	h = http.Header{"Content-Type": {"text/html"}}
	d.Policy.Apply(h, "/x.woff2", 200, true, time.Now()) // a script keeps its type
	if !strings.HasPrefix(h.Get("Content-Type"), "text/html") {
		t.Errorf("a script's type was overridden: %v", h)
	}
	h = http.Header{"Content-Type": {"text/plain"}}
	d.Policy.Apply(h, "/x.woff2", 404, false, time.Now())
	if h.Get("Content-Type") != "text/plain; charset=UTF-8" {
		t.Errorf("error pages: %v", h)
	}
	parent := compileClean(t, "AddDefaultCharset UTF-8")
	child := compileClean(t, "AddDefaultCharset Off")
	if got := Evaluate([]Layer{{DirURL: "/", File: parent}, {DirURL: "/a/", File: child}}, newReq(fakeFS{}, "/a/x", ""), false); got.Policy != nil {
		t.Error("AddDefaultCharset Off did not turn it off")
	}
}

func TestAddTypeCannotChooseAHandler(t *testing.T) {
	for _, src := range []string{
		"AddType application/x-httpd-php .txt", "AddType application/x-httpd-php5 .php", "AddType application/x-php .x",
		"AddType text/x-server-parsed-html .html", "AddType text/* .x", "AddType text/plain",
		"AddType text/plain ..", "AddDefaultCharset On", "AddDefaultCharset a;b",
	} {
		if len(compile(t, src).Problems()) == 0 {
			t.Errorf("%q was accepted", src)
		}
	}
}

func TestPHPSettings(t *testing.T) {
	d := eval(t, "php_value upload_max_filesize 64M\nphp_flag display_errors off\nphp_value memory_limit \"256M\"\nphp_value upload_max_filesize 8M\nphp_flag log_errors On", "/a.php")
	got := map[string]string{}
	for _, s := range d.PHP {
		got[s.Name] = s.Value
	}
	if len(d.PHP) != 4 || got["upload_max_filesize"] != "8M" || got["display_errors"] != "Off" || got["memory_limit"] != "256M" || got["log_errors"] != "On" {
		t.Errorf("%+v", d.PHP)
	}
	if d.PHP[0].Name != "upload_max_filesize" {
		t.Errorf("order changed: %+v", d.PHP)
	}
	for _, src := range []string{
		"php_value open_basedir /", "php_value OPEN_BASEDIR /", "php_value auto_prepend_file x.php", "php_value disable_functions \"\"",
		"php_value allow_url_include 1", "php_value error_log /etc/x", "php_value session.save_path /tmp", "php_value extension x.so",
		"php_value", "php_value a", "php_value a b c", "php_flag a maybe", "php_flag a on off",
		"php_value \"a=b\" 1", "php_value a \"it's\"", "php_admin_value memory_limit 1G", "php_admin_flag engine off",
	} {
		if len(compile(t, src).Problems()) == 0 {
			t.Errorf("%q was accepted", src)
		}
	}
}

func TestPHPSettingsFollowTheCategoryAllowList(t *testing.T) {
	nodes, probs := Parse([]byte("php_value memory_limit 1G"))
	c := Compile(nodes, probs, Options{Allow: map[string]bool{CatRewrite: true}})
	if p := c.Problems(); len(p) != 1 || p[0].Status != Disallowed {
		t.Errorf("%+v", p)
	}
	nodes, probs = Parse([]byte("ExpiresActive On\nAddType a/b .c\nAddDefaultCharset UTF-8"))
	if p := Compile(nodes, probs, Options{Allow: map[string]bool{CatPHP: true}}).Problems(); len(p) != 3 {
		t.Errorf("headers category gate: %+v", p)
	}
}

// ---- authentication ----

const secretSHA = "{SHA}5en6G6MezRroT3XKqkdPOmY/BfQ="

func TestPasswordHashes(t *testing.T) {
	const bcryptHash = "$2y$04$enFGCfVtuR9MFSVSZradKuMNRQ3r3odf898vtkENCRYfyx5UFLFTC"
	for _, tc := range []struct{ hash, pw string }{
		{"$apr1$r31.....$HqJZimcKQFAMYayBlzkrA/", "myPassword"}, // the example in Apache's documentation
		{"$apr1$abcdefgh$sWKZoXSk9w2pcQYiqj02q.", "p@ss w0rd long long password over sixteen chars"},
		{bcryptHash, "secret"}, // made by PHP's password_hash
		{secretSHA, "secret"},
	} {
		if !VerifyPassword(tc.hash, tc.pw) {
			t.Errorf("%s did not accept its password", tc.hash)
		}
		for _, wrong := range []string{"", tc.pw + "x", tc.pw[:len(tc.pw)-1], strings.ToUpper(tc.pw) + "!"} {
			if VerifyPassword(tc.hash, wrong) {
				t.Errorf("%s accepted %q", tc.hash, wrong)
			}
		}
	}
	for _, hash := range []string{"", "secret", "xyz", "$1$salt$hash", "$2x$04$enFGCfVtuR9MFSVSZradKuMNRQ3r3odf898vtkENCRYfyx5UFLFTC", "{SSHA}x", "$apr1$", "$apr1$$", "$2y$", "$2y$99$x", "{SHA}"} {
		for _, pw := range []string{"", "secret", hash} {
			if VerifyPassword(hash, pw) {
				t.Errorf("hash %q accepted %q", hash, pw)
			}
		}
	}
	if VerifyPassword(bcryptHash, strings.Repeat("a", 100)) {
		t.Error("a long wrong password was accepted")
	}
	start := time.Now()
	if VerifyPassword("$2y$31$enFGCfVtuR9MFSVSZradKuMNRQ3r3odf898vtkENCRYfyx5UFLFTC", "secret") || VerifyPassword("$2y$14$enFGCfVtuR9MFSVSZradKuMNRQ3r3odf898vtkENCRYfyx5UFLFTC", "secret") {
		t.Error("an entry with a huge cost matched")
	}
	if time.Since(start) > time.Second {
		t.Errorf("a hash with a huge cost was worked on for %v", time.Since(start))
	}
	if HashAPR1("x", "salt") == HashAPR1("y", "salt") {
		t.Error("the hash ignores the password")
	}
}

func TestParseHtpasswdAndBasic(t *testing.T) {
	u := ParseHtpasswd([]byte("# comment\n\nalice:{SHA}x\nbob:$apr1$a$b\r\nbad line\n:nouser\nnohash:\nalice:{SHA}second\n"))
	if len(u) != 2 || u["alice"] != "{SHA}x" || u["bob"] != "$apr1$a$b" {
		t.Errorf("%v", u)
	}
	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	for h, want := range map[string][3]string{
		"Basic " + enc("al:pw"):   {"al", "pw", "ok"},
		"basic " + enc("al:p:w"):  {"al", "p:w", "ok"},
		"BASIC  " + enc("al:"):    {"al", "", "ok"},
		"Basic " + enc("nocolon"): {"", "", ""},
		"Basic " + enc(":pw"):     {"", "", ""},
		"Basic !!!":               {"", "", ""},
		"Bearer " + enc("al:pw"):  {"", "", ""},
		"":                        {"", "", ""},
		"Basic":                   {"", "", ""},
	} {
		user, pass, ok := ParseBasic(h)
		if user != want[0] || pass != want[1] || ok != (want[2] == "ok") {
			t.Errorf("%q = %q %q %v", h, user, pass, ok)
		}
	}
}

// authReq is a request whose Basic credentials are checked against users.
func authReq(users map[string]string, header string) *Request {
	r := newReq(fakeFS{}, "/admin/page", "")
	r.Header = func(n string) string {
		if strings.EqualFold(n, "Authorization") {
			return header
		}
		return ""
	}
	r.BasicUser = func(file, dir string) string {
		u, p, ok := ParseBasic(header)
		if !ok {
			return ""
		}
		if hash, found := users[u]; found && VerifyPassword(hash, p) {
			return u
		}
		return ""
	}
	return r
}

func cred(u, p string) string { return "Basic " + base64.StdEncoding.EncodeToString([]byte(u+":"+p)) }

func TestBasicAuthentication(t *testing.T) {
	users := map[string]string{"alice": secretSHA, "bob": secretSHA}
	c := compileClean(t, "AuthType Basic\nAuthName \"Staff only\"\nAuthUserFile /srv/.htpasswd\nRequire valid-user")
	run := func(header string) *Decision { return Evaluate(rootLayer(c), authReq(users, header), false) }

	if d := run(""); d.Status != 401 || d.AuthRealm != "Staff only" {
		t.Errorf("no credentials: %+v", d)
	}
	if d := run(cred("alice", "wrong")); d.Status != 401 {
		t.Errorf("wrong password: %+v", d)
	}
	if d := run(cred("mallory", "secret")); d.Status != 401 {
		t.Errorf("unknown user: %+v", d)
	}
	if d := run(cred("alice", "secret")); d.Status != 0 || d.Env["REMOTE_USER"] != "alice" || d.Env["AUTH_TYPE"] != "Basic" {
		t.Errorf("good credentials: %+v", d)
	}

	c = compileClean(t, "AuthType Basic\nAuthUserFile /x\nRequire user bob carol")
	if d := Evaluate(rootLayer(c), authReq(users, cred("alice", "secret")), false); d.Status != 403 {
		t.Errorf("a valid user who isn't listed got in: %+v", d)
	}
	if d := Evaluate(rootLayer(c), authReq(users, cred("bob", "secret")), false); d.Status != 0 {
		t.Errorf("listed user: %+v", d)
	}
	if d := Evaluate(rootLayer(c), authReq(users, ""), false); d.AuthRealm != "Restricted" {
		t.Errorf("default realm: %+v", d)
	}
}

func TestAuthCombinedWithAddressRules(t *testing.T) {
	users := map[string]string{"alice": secretSHA}
	good := cred("alice", "secret")
	head := "AuthType Basic\nAuthUserFile /x\n"
	// Any of the Require lines: an address in the range needs no password.
	c := compileClean(t, head+"Require ip 203.0.113.0/24\nRequire valid-user")
	if d := Evaluate(rootLayer(c), authReq(users, ""), false); d.Status != 0 {
		t.Errorf("address alternative: %+v", d)
	}
	// RequireAll: both.
	c = compileClean(t, head+"<RequireAll>\nRequire ip 10.0.0.0/8\nRequire valid-user\n</RequireAll>")
	if d := Evaluate(rootLayer(c), authReq(users, good), false); d.Status != 401 && d.Status != 403 {
		t.Errorf("outside the range with a password: %+v", d)
	}
	c = compileClean(t, head+"<RequireAll>\nRequire ip 203.0.113.0/24\nRequire valid-user\n</RequireAll>")
	if d := Evaluate(rootLayer(c), authReq(users, ""), false); d.Status != 401 {
		t.Errorf("inside the range without a password: %+v", d)
	}
	if d := Evaluate(rootLayer(c), authReq(users, good), false); d.Status != 0 {
		t.Errorf("inside the range with a password: %+v", d)
	}
}

func TestAuthMisconfigurationFailsClosed(t *testing.T) {
	users := map[string]string{"alice": secretSHA}
	good := cred("alice", "secret")
	for _, src := range []string{"Require valid-user", "AuthType Basic\nRequire valid-user", "AuthUserFile /x\nRequire valid-user", "AuthName x\nRequire user alice",
		"Require ip 10.0.0.0/8\nRequire valid-user"} { // the client is outside the range, so the user rule is reached
		d := Evaluate(rootLayer(compileClean(t, src)), authReq(users, good), false)
		if d.Status != 500 || d.Why == "" {
			t.Errorf("%q: %+v", src, d)
		}
	}
}

func TestAuthInFilesBlockAndInheritance(t *testing.T) {
	// The password file is set in the parent, the rule in a <Files> block of the child.
	parent := compileClean(t, "AuthType Basic\nAuthName \"P\"\nAuthUserFile pw/.htpasswd")
	child := compileClean(t, "<Files \"secret.txt\">\nRequire valid-user\n</Files>")
	layers := []Layer{{DirURL: "/", File: parent}, {DirURL: "/a/", File: child}}
	var gotFile, gotDir string
	mk := func(path string) *Request {
		r := authReq(nil, "")
		r.Path = path
		r.BasicUser = func(f, d string) string { gotFile, gotDir = f, d; return "" }
		return r
	}
	if d := Evaluate(layers, mk("/a/secret.txt"), false); d.Status != 401 || d.AuthRealm != "P" {
		t.Errorf("%+v", d)
	}
	if gotFile != "pw/.htpasswd" || gotDir != "/" {
		t.Errorf("the user file is read relative to the file that named it: %q %q", gotFile, gotDir)
	}
	if d := Evaluate(layers, mk("/a/other.txt"), false); d.Status != 0 {
		t.Errorf("other files must stay open: %+v", d)
	}
}

func TestAuthDirectivesFollowTheAccessCategory(t *testing.T) {
	nodes, probs := Parse([]byte("AuthType Basic\nAuthName x\nAuthUserFile /x\nRequire valid-user"))
	c := Compile(nodes, probs, Options{Allow: map[string]bool{CatHeaders: true}})
	n := 0
	for _, p := range c.Problems() {
		if p.Status != Disallowed || !p.Sensitive {
			t.Errorf("%+v", p)
		}
		n++
	}
	if n != 4 {
		t.Errorf("%d problems", n)
	}
	for _, src := range []string{"AuthType Digest", "AuthType Basic Digest", "AuthBasicProvider ldap", "AuthName", "AuthUserFile", "AuthUserFile a b", "Require not valid-user", "Require valid-user x", "Require user", "Require not user a"} {
		ps := compile(t, src).Problems()
		if len(ps) == 0 || !ps[0].Sensitive {
			t.Errorf("%q: %+v", src, ps)
		}
	}
}

func TestIfModuleBlocksOfImplementedModulesApply(t *testing.T) {
	src := "<IfModule mod_expires.c>\nExpiresActive On\nExpiresByType image/jpeg \"access plus 1 year\"\n</IfModule>\n" +
		"<IfModule mod_mime.c>\nAddType font/woff2 .woff2\n</IfModule>\n" +
		"<IfModule mod_php7.c>\nphp_value memory_limit 1G\n</IfModule>\n" +
		"<IfModule !mod_expires.c>\nHeader set X-No-Expires 1\n</IfModule>\n"
	c := compileClean(t, src)
	d := Evaluate(rootLayer(c), newReq(fakeFS{}, "/a.jpg", ""), false)
	if d.Policy == nil || d.Policy.Expires == nil || d.Policy.Types[".woff2"] != "font/woff2" {
		t.Errorf("%+v", d.Policy)
	}
	// As with Apache and PHP-FPM, mod_php blocks are skipped; the negated block too.
	if len(d.PHP) != 0 || len(d.Headers) != 0 {
		t.Errorf("php %v headers %v", d.PHP, d.Headers)
	}
}
