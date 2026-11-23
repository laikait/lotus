package htaccess

import (
	"net/netip"
	"strings"
	"testing"
)

// fakeFS is the document root as the engine sees it: URL path -> directory?
type fakeFS map[string]bool

func (f fakeFS) stat(p string) (FileInfo, bool) {
	isDir, ok := f[p]
	if !ok {
		return FileInfo{}, false
	}
	return FileInfo{IsDir: isDir, Size: 10}, true
}

const root = "/srv/www"

func newReq(fs fakeFS, path, query string) *Request {
	return &Request{
		Method: "GET", Host: "example.test", Scheme: "http", Proto: "HTTP/1.1",
		Remote: netip.MustParseAddr("203.0.113.9"), ServerPort: "80",
		RequestLine: "GET " + path + " HTTP/1.1",
		Path:        path, Query: query,
		Header: func(string) string { return "" },
		Root:   root, Stat: fs.stat,
	}
}

func compile(t *testing.T, src string) *Compiled {
	t.Helper()
	nodes, problems := Parse([]byte(src))
	return Compile(nodes, problems, Options{})
}

// compileClean compiles and fails the test if anything was not supported.
func compileClean(t *testing.T, src string) *Compiled {
	t.Helper()
	c := compile(t, src)
	for _, l := range c.Problems() {
		t.Fatalf("line %d %s: %s", l.Line, l.Directive, l.Msg)
	}
	return c
}

func rootLayer(c *Compiled) []Layer { return []Layer{{DirURL: "/", File: c}} }

// ---- parser ----

func TestParseSyntax(t *testing.T) {
	src := "# comment\r\n" +
		"  RewriteRule   ^a\\.php$   \"/b c\"  [L]  \n" +
		"Header set X-A \"say \\\"hi\\\"\"\n" +
		"Long one \\\n  continued\n" +
		"<IfModule mod_rewrite.c>\n  <Files 'x.txt'>\n    Require all denied\n  </Files>\n</IfModule>\n"
	nodes, problems := Parse([]byte(src))
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	if len(nodes) != 4 {
		t.Fatalf("%d nodes", len(nodes))
	}
	if n := nodes[0]; n.Name != "rewriterule" || len(n.Args) != 3 || n.Args[0] != `^a\.php$` || n.Args[1] != "/b c" || n.Line != 2 {
		t.Errorf("rule: %+v", n)
	}
	if n := nodes[1]; n.Args[2] != `say "hi"` || !n.Quoted[2] {
		t.Errorf("escaped quote: %+v", n)
	}
	if n := nodes[2]; n.Name != "long" || len(n.Args) != 2 || n.Args[1] != "continued" {
		t.Errorf("continuation: %+v", n)
	}
	im := nodes[3]
	if !im.Container || im.Name != "ifmodule" || len(im.Children) != 1 || im.Children[0].Name != "files" || len(im.Children[0].Children) != 1 {
		t.Errorf("containers: %+v", im)
	}
}

func TestParseProblems(t *testing.T) {
	for name, src := range map[string]string{
		"unclosed quote":     `Header set X "abc`,
		"unclosed container": "<IfModule a>\nRewriteEngine On\n",
		"wrong closer":       "<IfModule a>\n</Files>\n",
		"stray closer":       "</IfModule>\n",
		"no >":               "<IfModule a\n",
		"too deep":           strings.Repeat("<IfModule a>\n", 20),
	} {
		if _, problems := Parse([]byte(src)); len(problems) == 0 {
			t.Errorf("%s: no problem reported", name)
		}
	}
	if _, p := Parse(make([]byte, MaxFileSize+1)); len(p) == 0 {
		t.Error("an oversized file was accepted")
	}
}

func TestSyntaxErrorsAreSensitive(t *testing.T) {
	c := compile(t, "Require all granted\nRequire ip \"10.0.0.0/8\n")
	var found bool
	for _, l := range c.Problems() {
		if l.Directive == "(syntax)" && l.Sensitive {
			found = true
		}
	}
	if !found {
		t.Error("a line that didn't parse must count as sensitive: it might have been an access rule")
	}
}

// ---- real files ----

const wordpress = `# BEGIN WordPress
<IfModule mod_rewrite.c>
RewriteEngine On
RewriteBase /
RewriteRule ^index\.php$ - [L]
RewriteCond %{REQUEST_FILENAME} !-f
RewriteCond %{REQUEST_FILENAME} !-d
RewriteRule . /index.php [L]
</IfModule>
# END WordPress
`

func TestWordPress(t *testing.T) {
	c := compileClean(t, wordpress)
	fs := fakeFS{"/": true, "/index.php": false, "/wp-content": true, "/wp-content/a.css": false, "/wp-admin": true}
	for _, tc := range []struct {
		path, query string
		want        string // "", or the rewritten path
	}{
		{"/hello-world/", "", "/index.php"},
		{"/2024/05/post", "x=1", "/index.php"},
		{"/wp-content/a.css", "", ""}, // a file
		{"/wp-admin/", "", ""},        // a directory
		{"/index.php", "", ""},        // guarded by the first rule
		{"/", "", ""},                 // "." needs a character
		{"/wp-content/missing.css", "", "/index.php"},
	} {
		d := Evaluate(rootLayer(c), newReq(fs, tc.path, tc.query), false)
		got := ""
		if d.Rewrite != nil {
			got = d.Rewrite.Path
			if d.Rewrite.Query != tc.query {
				t.Errorf("%s: the query string was %q, want it kept as %q", tc.path, d.Rewrite.Query, tc.query)
			}
		}
		if got != tc.want || d.Status != 0 || d.Redirect != nil {
			t.Errorf("%s -> rewrite %q status %d redirect %v, want %q", tc.path, got, d.Status, d.Redirect, tc.want)
		}
	}
	// The second pass (the server routes /index.php again) must not loop.
	if d := Evaluate(rootLayer(c), newReq(fs, "/index.php", ""), false); d.Rewrite != nil {
		t.Error("the rewritten URL was rewritten again")
	}
}

const laravel = `<IfModule mod_rewrite.c>
    <IfModule mod_negotiation.c>
        Options -MultiViews -Indexes
    </IfModule>

    RewriteEngine On

    # Handle Authorization Header
    RewriteCond %{HTTP:Authorization} .
    RewriteRule .* - [E=HTTP_AUTHORIZATION:%{HTTP:Authorization}]

    # Redirect Trailing Slashes If Not A Folder...
    RewriteCond %{REQUEST_FILENAME} !-d
    RewriteCond %{REQUEST_URI} (.+)/$
    RewriteRule ^ %1 [L,R=301]

    # Send Requests To Front Controller...
    RewriteCond %{REQUEST_FILENAME} !-d
    RewriteCond %{REQUEST_FILENAME} !-f
    RewriteRule ^ index.php [L]
</IfModule>
`

func TestLaravel(t *testing.T) {
	c := compileClean(t, laravel)
	fs := fakeFS{"/": true, "/index.php": false, "/css": true, "/css/app.css": false}
	run := func(path string, hdr map[string]string) *Decision {
		r := newReq(fs, path, "")
		r.Header = func(n string) string { return hdr[n] }
		return Evaluate(rootLayer(c), r, false)
	}
	if d := run("/posts/", nil); d.Redirect == nil || d.Redirect.Status != 301 || d.Redirect.Location != "/posts" {
		t.Errorf("trailing slash: %+v", d)
	}
	if d := run("/css/", nil); d.Redirect != nil || d.Rewrite != nil {
		t.Errorf("a directory keeps its slash: %+v", d)
	}
	if d := run("/posts", nil); d.Rewrite == nil || d.Rewrite.Path != "/index.php" {
		t.Errorf("front controller (a relative substitution gets the base): %+v", d)
	}
	if d := run("/css/app.css", nil); d.Rewrite != nil || d.Redirect != nil {
		t.Errorf("a file is served as it is: %+v", d)
	}
	d := run("/api/x", map[string]string{"Authorization": "Bearer abc"})
	if d.Env["HTTP_AUTHORIZATION"] != "Bearer abc" {
		t.Errorf("E flag: %v", d.Env)
	}
	if d.Rewrite == nil {
		t.Error("the environment rule has no L, so the rules after it must still run")
	}
}

const drupalish = `
# Protect files and directories from prying eyes.
<FilesMatch "\.(engine|inc|install|module|sh|sql|theme|twig|tpl(\.php)?|xtmpl|yml)(~|\.sw[op]|\.bak|\.orig|\.save)?$|^(Entries.*|Repository|Root|Tag|Template|composer\.(json|lock)|web\.config)$|^#.*#$|\.php(~|\.sw[op]|\.bak|\.orig|\.save)$">
  <IfModule mod_authz_core.c>
    Require all denied
  </IfModule>
  <IfModule !mod_authz_core.c>
    Order allow,deny
  </IfModule>
</FilesMatch>

Options -Indexes
DirectoryIndex index.php index.html
ErrorDocument 404 /index.php
ErrorDocument 403 "Access denied"

<IfModule mod_expires.c>
  ExpiresActive On
  ExpiresByType image/png "access plus 1 year"
</IfModule>
<IfModule mod_headers.c>
  Header set X-Content-Type-Options "nosniff"
  Header always set X-Frame-Options "SAMEORIGIN"
</IfModule>
`

func TestDrupalStyleFile(t *testing.T) {
	c := compileClean(t, drupalish)
	var expiresNote bool
	for _, n := range c.Notes {
		expiresNote = expiresNote || strings.Contains(n.Msg, "mod_expires")
	}
	if !expiresNote {
		t.Errorf("the skipped mod_expires block must be reported: %v", c.Notes)
	}
	fs := fakeFS{"/": true, "/index.php": false, "/core.module": false, "/composer.json": false, "/style.css": false}
	for path, wantDenied := range map[string]bool{
		"/core.module": true, "/composer.json": true, "/settings.php.bak": true, "/style.css": false, "/index.php": false,
	} {
		d := Evaluate(rootLayer(c), newReq(fs, path, ""), false)
		if (d.Status == 403) != wantDenied {
			t.Errorf("%s: status %d, want denied=%v", path, d.Status, wantDenied)
		}
	}
	d := Evaluate(rootLayer(c), newReq(fs, "/style.css", ""), false)
	if !d.IndexSet || len(d.Index) != 2 || d.Index[0] != "index.php" {
		t.Errorf("DirectoryIndex: %v %v", d.IndexSet, d.Index)
	}
	if doc := d.ErrorDocs[404]; doc.Kind != DocPath || doc.Value != "/index.php" {
		t.Errorf("ErrorDocument 404: %+v", doc)
	}
	if doc := d.ErrorDocs[403]; doc.Kind != DocText || doc.Value != "Access denied" {
		t.Errorf("ErrorDocument 403: %+v", doc)
	}
	if len(d.Headers) != 2 || d.Headers[0].Name != "X-Content-Type-Options" {
		t.Errorf("headers: %+v", d.Headers)
	}
}

// ---- rewrite semantics ----

func rw(t *testing.T, rules string, path, query string, fs fakeFS) *Decision {
	t.Helper()
	c := compileClean(t, "RewriteEngine On\n"+rules)
	return Evaluate(rootLayer(c), newReq(fs, path, query), false)
}

func TestRewriteFlags(t *testing.T) {
	fs := fakeFS{}
	t.Run("redirect with a code", func(t *testing.T) {
		d := rw(t, "RewriteRule ^old/(.*)$ /new/$1 [R=301,L]", "/old/a b", "x=1", fs)
		if d.Redirect == nil || d.Redirect.Status != 301 || d.Redirect.Location != "/new/a%20b?x=1" {
			t.Errorf("%+v", d.Redirect)
		}
	})
	t.Run("plain R is a 302", func(t *testing.T) {
		if d := rw(t, "RewriteRule ^a$ /b [R]", "/a", "", fs); d.Redirect == nil || d.Redirect.Status != 302 {
			t.Errorf("%+v", d)
		}
	})
	t.Run("absolute URL is always a redirect", func(t *testing.T) {
		d := rw(t, "RewriteRule ^go$ https://other.test/x", "/go", "q=1", fs)
		if d.Redirect == nil || d.Redirect.Location != "https://other.test/x?q=1" || d.Redirect.Status != 302 {
			t.Errorf("%+v", d.Redirect)
		}
	})
	t.Run("forbidden and gone", func(t *testing.T) {
		if d := rw(t, `RewriteRule \.bak$ - [F]`, "/a.bak", "", fs); d.Status != 403 {
			t.Errorf("F: %d", d.Status)
		}
		if d := rw(t, `RewriteRule ^old$ - [G]`, "/old", "", fs); d.Status != 410 {
			t.Errorf("G: %d", d.Status)
		}
	})
	t.Run("NC", func(t *testing.T) {
		if d := rw(t, "RewriteRule ^ABC$ /x [NC,L]", "/abc", "", fs); d.Rewrite == nil {
			t.Error("NC did not ignore the case")
		}
		if d := rw(t, "RewriteRule ^ABC$ /x [L]", "/abc", "", fs); d.Rewrite != nil {
			t.Error("without NC the pattern is case-sensitive")
		}
	})
	t.Run("query string handling", func(t *testing.T) {
		cases := []struct{ rule, query, want string }{
			{"RewriteRule ^a$ /b", "x=1", "x=1"},     // kept
			{"RewriteRule ^a$ /b?y=2", "x=1", "y=2"}, // replaced
			{"RewriteRule ^a$ /b?y=2 [QSA]", "x=1", "y=2&x=1"},
			{"RewriteRule ^a$ /b [QSD]", "x=1", ""},
			{"RewriteRule ^a$ /b?y=2 [QSA]", "", "y=2"},
		}
		for _, tc := range cases {
			d := rw(t, tc.rule, "/a", tc.query, fs)
			if d.Rewrite == nil || d.Rewrite.Query != tc.want {
				t.Errorf("%s with %q: %+v, want query %q", tc.rule, tc.query, d.Rewrite, tc.want)
			}
		}
	})
	t.Run("rules continue after a change until L", func(t *testing.T) {
		d := rw(t, "RewriteRule ^a$ b\nRewriteRule ^b$ c [L]\nRewriteRule ^c$ d", "/a", "", fs)
		if d.Rewrite == nil || d.Rewrite.Path != "/c" {
			t.Errorf("%+v", d.Rewrite)
		}
	})
	t.Run("skip", func(t *testing.T) {
		d := rw(t, "RewriteRule ^a$ - [S=1]\nRewriteRule ^a$ /skipped [L]\nRewriteRule ^a$ /reached [L]", "/a", "", fs)
		if d.Rewrite == nil || d.Rewrite.Path != "/reached" {
			t.Errorf("%+v", d.Rewrite)
		}
	})
	t.Run("chain", func(t *testing.T) {
		rules := "RewriteRule ^a$ - [C]\nRewriteRule ^zzz$ /never [L]\nRewriteRule ^a$ /after [L]"
		d := rw(t, rules, "/a", "", fs)
		if d.Rewrite == nil || d.Rewrite.Path != "/after" {
			t.Errorf("a failed chain must skip its tail and let the next rule run: %+v", d.Rewrite)
		}
	})
	t.Run("END stops further rewriting", func(t *testing.T) {
		d := rw(t, "RewriteRule ^a$ /b [END]", "/a", "", fs)
		if d.Rewrite == nil || !d.Rewrite.End {
			t.Errorf("%+v", d.Rewrite)
		}
	})
	t.Run("a negated pattern", func(t *testing.T) {
		if d := rw(t, "RewriteRule !^api/ /app.php [L]", "/page", "", fs); d.Rewrite == nil {
			t.Error("!pattern should match what the pattern doesn't")
		}
		if d := rw(t, "RewriteRule !^api/ /app.php [L]", "/api/x", "", fs); d.Rewrite != nil {
			t.Error("!pattern matched a path the pattern matches")
		}
	})
}

func TestRewriteConditions(t *testing.T) {
	fs := fakeFS{"/f": false, "/d": true, "/etc/passwd": false} // a file that exists, but not below the document root
	run := func(conds, path string, hdr map[string]string) bool {
		c := compileClean(t, "RewriteEngine On\n"+conds+"\nRewriteRule .* /hit [L]")
		r := newReq(fs, path, "")
		r.Header = func(n string) string { return hdr[n] }
		return Evaluate(rootLayer(c), r, false).Rewrite != nil
	}
	if !run("RewriteCond %{REQUEST_FILENAME} !-f", "/missing", nil) || run("RewriteCond %{REQUEST_FILENAME} !-f", "/f", nil) {
		t.Error("-f")
	}
	if !run("RewriteCond %{REQUEST_FILENAME} -d", "/d", nil) || run("RewriteCond %{REQUEST_FILENAME} -d", "/f", nil) {
		t.Error("-d")
	}
	if !run("RewriteCond %{DOCUMENT_ROOT}%{REQUEST_URI} -f", "/f", nil) {
		t.Error("-f on DOCUMENT_ROOT + REQUEST_URI")
	}
	if run("RewriteCond /etc/passwd -f", "/x", nil) {
		t.Error("a file test outside the document root must be false")
	}
	if !run("RewriteCond %{HTTP_HOST} ^www\\.", "/x", map[string]string{"Host": "x"}) == false {
		// Host comes from Request.Host, not the header function.
		t.Log("HTTP_HOST reads Request.Host")
	}
	if !run("RewriteCond %{HTTP:X-Test} =yes", "/x", map[string]string{"X-Test": "yes"}) || run("RewriteCond %{HTTP:X-Test} =yes", "/x", nil) {
		t.Error("string equality")
	}
	if !run("RewriteCond %{HTTP:X-Test} =YES [NC]", "/x", map[string]string{"X-Test": "yes"}) {
		t.Error("NC on a string comparison")
	}
	// OR joins neighbours; AND is the default.
	two := "RewriteCond %{HTTP:A} =1 [OR]\nRewriteCond %{HTTP:B} =1"
	if !run(two, "/x", map[string]string{"B": "1"}) || !run(two, "/x", map[string]string{"A": "1"}) || run(two, "/x", nil) {
		t.Error("[OR]")
	}
	and := "RewriteCond %{HTTP:A} =1\nRewriteCond %{HTTP:B} =1"
	if run(and, "/x", map[string]string{"A": "1"}) || !run(and, "/x", map[string]string{"A": "1", "B": "1"}) {
		t.Error("conditions are ANDed by default")
	}
}

func TestBackReferencesInConditions(t *testing.T) {
	c := compileClean(t, "RewriteEngine On\nRewriteCond %{HTTP_HOST} ^(www\\.)?(.+)$ [NC]\nRewriteRule ^(.*)$ http://%2/$1 [R=301,L]")
	r := newReq(fakeFS{}, "/page", "")
	r.Host = "www.example.test"
	d := Evaluate(rootLayer(c), r, false)
	if d.Redirect == nil || d.Redirect.Location != "http://example.test/page" {
		t.Errorf("%+v", d.Redirect)
	}
}

func TestRewriteInASubdirectory(t *testing.T) {
	blog := compileClean(t, "RewriteEngine On\nRewriteRule ^x$ y [L]\nRewriteRule ^deep/(.*)$ /other/$1 [L]")
	layers := []Layer{{DirURL: "/blog/", File: blog}}
	fs := fakeFS{}
	// The pattern sees the path below the directory; a relative target gets the directory's URL.
	if d := Evaluate(layers, newReq(fs, "/blog/x", ""), false); d.Rewrite == nil || d.Rewrite.Path != "/blog/y" {
		t.Errorf("%+v", d.Rewrite)
	}
	if d := Evaluate(layers, newReq(fs, "/blog/deep/a", ""), false); d.Rewrite == nil || d.Rewrite.Path != "/other/a" {
		t.Errorf("%+v", d.Rewrite)
	}
	// RewriteBase replaces the default.
	based := compileClean(t, "RewriteEngine On\nRewriteBase /app\nRewriteRule ^x$ y [L]")
	if d := Evaluate([]Layer{{DirURL: "/blog/", File: based}}, newReq(fs, "/blog/x", ""), false); d.Rewrite == nil || d.Rewrite.Path != "/app/y" {
		t.Errorf("%+v", d.Rewrite)
	}
}

func TestChildRewriteRulesReplaceTheParents(t *testing.T) {
	parent := compileClean(t, "RewriteEngine On\nRewriteRule ^sub/a$ /parent-rule [L]")
	child := compileClean(t, "RewriteEngine On\nRewriteRule ^b$ /child-rule [L]")
	silent := compileClean(t, "Options -Indexes\n")
	fs := fakeFS{}
	layers := []Layer{{"/", parent}, {"/sub/", child}}
	if d := Evaluate(layers, newReq(fs, "/sub/a", ""), false); d.Rewrite != nil {
		t.Errorf("the parent's rule ran although the child directory has its own: %+v", d.Rewrite)
	}
	if d := Evaluate(layers, newReq(fs, "/sub/b", ""), false); d.Rewrite == nil || d.Rewrite.Path != "/child-rule" {
		t.Errorf("%+v", d.Rewrite)
	}
	// A child with no rewrite directives at all leaves the parent's in force.
	layers = []Layer{{"/", parent}, {"/sub/", silent}}
	if d := Evaluate(layers, newReq(fs, "/sub/a", ""), false); d.Rewrite == nil || d.Rewrite.Path != "/parent-rule" {
		t.Errorf("%+v", d.Rewrite)
	}
	// RewriteEngine Off in a child turns it off.
	off := compileClean(t, "RewriteEngine Off\n")
	layers = []Layer{{"/", parent}, {"/sub/", off}}
	if d := Evaluate(layers, newReq(fs, "/sub/a", ""), false); d.Rewrite != nil {
		t.Errorf("%+v", d.Rewrite)
	}
}

func TestRewriteNeedsTheEngineOn(t *testing.T) {
	c := compileClean(t, "RewriteRule ^a$ /b [L]")
	if d := Evaluate(rootLayer(c), newReq(fakeFS{}, "/a", ""), false); d.Rewrite != nil {
		t.Error("rules ran without RewriteEngine On")
	}
}

func TestNoRewriteAfterEND(t *testing.T) {
	c := compileClean(t, "RewriteEngine On\nRewriteRule ^a$ /b [L]\nRedirect /gone-soon /elsewhere")
	if d := Evaluate(rootLayer(c), newReq(fakeFS{}, "/a", ""), true); d.Rewrite != nil {
		t.Error("rewriting continued after END")
	}
	if d := Evaluate(rootLayer(c), newReq(fakeFS{}, "/gone-soon", ""), true); d.Redirect == nil {
		t.Error("END stops rewrite rules, not mod_alias redirects")
	}
}

// ---- redirects, error documents ----

func TestRedirects(t *testing.T) {
	c := compileClean(t, `Redirect /old /new
Redirect 301 /moved https://other.test/here
Redirect permanent /perm /p
Redirect gone /dead
RedirectMatch ^/blog/(\d+)$ /posts/$1
RedirectTemp /t /x
`)
	run := func(path, query string) *Decision {
		return Evaluate(rootLayer(c), newReq(fakeFS{}, path, query), false)
	}
	for _, tc := range []struct {
		path, query string
		status      int
		loc         string
	}{
		{"/old", "", 302, "/new"},
		{"/old/page", "a=1", 302, "/new/page?a=1"}, // the rest of the path and the query string are kept
		{"/moved/x", "", 301, "https://other.test/here/x"},
		{"/perm", "", 301, "/p"},
		{"/blog/42", "x=1", 302, "/posts/42"}, // RedirectMatch doesn't add the query
		{"/t", "", 302, "/x"},
	} {
		d := run(tc.path, tc.query)
		if d.Redirect == nil || d.Redirect.Status != tc.status || d.Redirect.Location != tc.loc {
			t.Errorf("%s?%s: %+v, want %d %s", tc.path, tc.query, d.Redirect, tc.status, tc.loc)
		}
	}
	if d := run("/oldest", ""); d.Redirect != nil {
		t.Error("/old matched /oldest: a prefix must end at a path segment")
	}
	if d := run("/dead", ""); d.Status != 410 {
		t.Errorf("gone: %d", d.Status)
	}
	if d := run("/other", ""); d.Redirect != nil || d.Status != 0 {
		t.Error("an unrelated path was redirected")
	}
}

func TestInnerRedirectsComeFirst(t *testing.T) {
	outer := compileClean(t, "Redirect /a/b /outer")
	inner := compileClean(t, "Redirect /a/b /inner")
	d := Evaluate([]Layer{{"/", outer}, {"/a/", inner}}, newReq(fakeFS{}, "/a/b", ""), false)
	if d.Redirect == nil || d.Redirect.Location != "/inner" {
		t.Errorf("%+v", d.Redirect)
	}
}

func TestErrorDocumentsMergeDownwards(t *testing.T) {
	outer := compileClean(t, "ErrorDocument 404 /e404.html\nErrorDocument 500 \"Oops\"")
	inner := compileClean(t, "ErrorDocument 404 https://elsewhere.test/nf")
	d := Evaluate([]Layer{{"/", outer}, {"/a/", inner}}, newReq(fakeFS{}, "/a/x", ""), false)
	if d.ErrorDocs[404] != (ErrorDoc{DocURL, "https://elsewhere.test/nf"}) || d.ErrorDocs[500] != (ErrorDoc{DocText, "Oops"}) {
		t.Errorf("%+v", d.ErrorDocs)
	}
}

// ---- access ----

func access(t *testing.T, src, path, ip string) int {
	t.Helper()
	c := compileClean(t, src)
	r := newReq(fakeFS{}, path, "")
	r.Remote = netip.MustParseAddr(ip)
	return Evaluate(rootLayer(c), r, false).Status
}

func TestRequire(t *testing.T) {
	for _, tc := range []struct {
		name, src, ip string
		want          int
	}{
		{"all granted", "Require all granted", "1.2.3.4", 0},
		{"all denied", "Require all denied", "1.2.3.4", 403},
		{"ip allows", "Require ip 10.0.0.0/8 192.168.1.5", "10.9.9.9", 0},
		{"ip refuses", "Require ip 10.0.0.0/8", "11.0.0.1", 403},
		{"several lines are alternatives", "Require ip 10.0.0.0/8\nRequire ip 172.16.0.0/12", "172.20.1.1", 0},
		{"ipv6", "Require ip 2001:db8::/32", "2001:db8::5", 0},
		{"ipv4-mapped ipv6 is the ipv4 address", "Require ip 10.0.0.0/8", "::ffff:10.1.1.1", 0},
		{"partial address", "Require ip 192.168", "192.168.7.7", 0},
		{"netmask", "Require ip 10.0.0.0/255.0.0.0", "10.2.3.4", 0},
		{"RequireAll with not ip", "<RequireAll>\nRequire all granted\nRequire not ip 203.0.113.0/24\n</RequireAll>", "203.0.113.9", 403},
		{"RequireAll with not ip, other client", "<RequireAll>\nRequire all granted\nRequire not ip 203.0.113.0/24\n</RequireAll>", "198.51.100.1", 0},
		{"RequireAny", "<RequireAny>\nRequire ip 10.0.0.1\nRequire ip 10.0.0.2\n</RequireAny>", "10.0.0.2", 0},
		{"RequireNone", "<RequireNone>\nRequire ip 10.0.0.1\n</RequireNone>", "10.0.0.1", 403},
		{"legacy deny from all", "Order allow,deny\nDeny from all", "1.2.3.4", 403},
		{"legacy allow then deny", "Order allow,deny\nAllow from 10.0.0.0/8\nDeny from 10.1.0.0/16", "10.1.2.3", 403},
		{"legacy allow then deny, allowed", "Order allow,deny\nAllow from 10.0.0.0/8\nDeny from 10.1.0.0/16", "10.2.2.2", 0},
		{"legacy deny,allow default is allow", "Order deny,allow\nDeny from 10.0.0.0/8", "11.0.0.1", 0},
		{"legacy deny,allow", "Order deny,allow\nDeny from 10.0.0.0/8", "10.0.0.1", 403},
		{"legacy deny,allow with an exception", "Order deny,allow\nDeny from all\nAllow from 127.0.0.1", "127.0.0.1", 0},
		{"legacy allow,deny with nothing allowed", "Order allow,deny", "1.2.3.4", 403},
	} {
		if got := access(t, tc.src, "/x", tc.ip); got != tc.want {
			t.Errorf("%s: status %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestAccessInFilesBlocks(t *testing.T) {
	src := "<Files \"wp-config.php\">\nRequire all denied\n</Files>\n<FilesMatch \"\\.(sql|bak)$\">\nRequire all denied\n</FilesMatch>\n<Files \"*.inc\">\nRequire all denied\n</Files>"
	for path, want := range map[string]int{
		"/wp-config.php": 403, "/a/wp-config.php": 403, "/dump.sql": 403, "/x/y.bak": 403, "/lib.inc": 403,
		"/index.php": 0, "/wp-config.php.txt": 0, "/dir/": 0,
	} {
		if got := access(t, src, path, "1.2.3.4"); got != want {
			t.Errorf("%s: %d, want %d", path, got, want)
		}
	}
}

func TestLaterAccessRulesReplaceEarlierOnes(t *testing.T) {
	outer := compileClean(t, "Require all denied")
	inner := compileClean(t, "Require all granted")
	r := newReq(fakeFS{}, "/open/x", "")
	if d := Evaluate([]Layer{{"/", outer}, {"/open/", inner}}, r, false); d.Status != 0 {
		t.Errorf("a directory that grants access can't be closed by its parent: %d", d.Status)
	}
	if d := Evaluate([]Layer{{"/", inner}, {"/closed/", outer}}, newReq(fakeFS{}, "/closed/x", ""), false); d.Status != 403 {
		t.Errorf("a directory that denies access: %d", d.Status)
	}
	// A Files block overrides the directory-level rule for its files.
	both := compileClean(t, "Require all denied\n<Files \"public.txt\">\nRequire all granted\n</Files>")
	if d := Evaluate(rootLayer(both), newReq(fakeFS{}, "/public.txt", ""), false); d.Status != 0 {
		t.Errorf("Files override: %d", d.Status)
	}
	if d := Evaluate(rootLayer(both), newReq(fakeFS{}, "/other.txt", ""), false); d.Status != 403 {
		t.Errorf("directory rule: %d", d.Status)
	}
}

func TestAccessIsCheckedBeforeRewriting(t *testing.T) {
	c := compileClean(t, "Require ip 10.0.0.0/8\nRewriteEngine On\nRewriteRule ^a$ /b [L]")
	r := newReq(fakeFS{}, "/a", "")
	d := Evaluate(rootLayer(c), r, false)
	if d.Status != 403 || d.Rewrite != nil {
		t.Errorf("%+v", d)
	}
}

// ---- headers ----

func TestHeaders(t *testing.T) {
	c := compileClean(t, "Header set X-A 1\nHeader always set X-B 2\nHeader unset Server\nHeader append Cache-Control \"no-store\"\n<Files \"a.json\">\nHeader set Content-Type application/json\n</Files>")
	d := Evaluate(rootLayer(c), newReq(fakeFS{}, "/x.html", ""), false)
	if len(d.Headers) != 4 || d.Headers[1] != (HeaderOp{"set", "X-B", "2"}) || d.Headers[2].Op != "unset" {
		t.Errorf("%+v", d.Headers)
	}
	d = Evaluate(rootLayer(c), newReq(fakeFS{}, "/a.json", ""), false)
	if len(d.Headers) != 5 {
		t.Errorf("a Files block's headers apply to its files: %+v", d.Headers)
	}
}

// ---- what is not supported ----

func TestUnsupportedDirectivesAreReported(t *testing.T) {
	for name, tc := range map[string]struct {
		src       string
		sensitive bool
	}{
		"php_value":           {"php_value upload_max_filesize 64M", false},
		"expires":             {"ExpiresActive On", false},
		"auth":                {"AuthType Basic", true},
		"require valid-user":  {"Require valid-user", true},
		"require host":        {"Require host example.com", true},
		"allow from a host":   {"Order allow,deny\nAllow from example.com", true},
		"satisfy":             {"Satisfy any", true},
		"Limit":               {"<Limit GET>\nRequire all denied\n</Limit>", true},
		"lookahead":           {"RewriteEngine On\nRewriteRule ^(?!api)(.*)$ /x", false},
		"backreference":       {"RewriteEngine On\nRewriteRule ^(a)\\1$ /x", false},
		"proxy flag":          {"RewriteEngine On\nRewriteRule ^(.*)$ http://b/$1 [P]", false},
		"handler flag":        {"RewriteEngine On\nRewriteRule .* - [H=x]", false},
		"unknown variable":    {"RewriteEngine On\nRewriteCond %{TIME_YEAR} 2020\nRewriteRule .* /x", false},
		"+Indexes":            {"Options +Indexes", false},
		"Options All":         {"Options All", false},
		"header expression":   {"Header set X \"%{UNIQUE_ID}e\"", false},
		"header env=":         {"Header set X y env=foo", false},
		"AddHandler":          {"AddHandler application/x-httpd-php .php", false},
		"not alone":           {"Require not ip 1.2.3.4", true},
		"mixed access styles": {"Require all granted\nOrder allow,deny", true},
		"RewriteMap":          {"RewriteMap m txt:/x", false},
		"cond without rule":   {"RewriteCond %{HTTP_HOST} x", false},
		"file test -l":        {"RewriteEngine On\nRewriteCond %{REQUEST_FILENAME} -l\nRewriteRule .* /x", false},
		"nested Files":        {"<Files a>\n<Files b>\n</Files>\n</Files>", false},
		"DirectoryIndex path": {"DirectoryIndex /abs/index.php", false},
	} {
		c := compile(t, tc.src)
		probs := c.Problems()
		if len(probs) == 0 {
			t.Errorf("%s: nothing was reported", name)
			continue
		}
		sensitive := false
		for _, p := range probs {
			sensitive = sensitive || p.Sensitive
		}
		if sensitive != tc.sensitive {
			t.Errorf("%s: sensitive = %v, want %v (%+v)", name, sensitive, tc.sensitive, probs)
		}
	}
}

func TestARefusedConditionDoesNotLetItsRuleRun(t *testing.T) {
	c := compile(t, "RewriteEngine On\nRewriteCond %{REQUEST_FILENAME} -l\nRewriteRule .* /index.php [L]")
	d := Evaluate(rootLayer(c), newReq(fakeFS{}, "/x", ""), false)
	if d.Rewrite != nil {
		t.Error("a rule whose condition is unsupported ran unconditionally")
	}
	if len(c.Problems()) < 2 {
		t.Errorf("both lines should be reported: %+v", c.Problems())
	}
}

func TestAllowList(t *testing.T) {
	nodes, probs := Parse([]byte("RewriteEngine On\nRewriteRule ^a$ /b [L]\nHeader set X 1\nRequire all denied\n"))
	c := Compile(nodes, probs, Options{Allow: map[string]bool{CatRewrite: true}})
	var disallowed []string
	for _, l := range c.Problems() {
		if l.Status == Disallowed {
			disallowed = append(disallowed, strings.ToLower(l.Directive))
			if strings.EqualFold(l.Directive, "require") && !l.Sensitive {
				t.Error("a refused Require must be sensitive")
			}
		}
	}
	if strings.Join(disallowed, ",") != "header,require" {
		t.Errorf("disallowed: %v", disallowed)
	}
	// The allowed part still works.
	if d := Evaluate(rootLayer(c), newReq(fakeFS{}, "/a", ""), false); d.Rewrite == nil {
		t.Error("the allowed rewrite rule did not run")
	}
}

func TestIfModule(t *testing.T) {
	c := compile(t, "<IfModule mod_rewrite.c>\nRewriteEngine On\nRewriteRule ^a$ /b [L]\n</IfModule>\n"+
		"<IfModule !mod_rewrite.c>\nRewriteRule ^a$ /wrong [L]\n</IfModule>\n"+
		"<IfModule mod_php7.c>\nphp_value x y\n</IfModule>\n"+
		"<IfModule !mod_php7.c>\nHeader set X-Fallback 1\n</IfModule>\n"+
		"<IfModule rewrite_module>\nRewriteBase /\n</IfModule>")
	if probs := c.Problems(); len(probs) != 0 {
		t.Fatalf("%+v", probs)
	}
	d := Evaluate(rootLayer(c), newReq(fakeFS{}, "/a", ""), false)
	if d.Rewrite == nil || d.Rewrite.Path != "/b" || len(d.Headers) != 1 {
		t.Errorf("%+v", d)
	}
	if len(c.Notes) != 2 {
		t.Errorf("notes: %+v", c.Notes)
	}
}

func TestOptions(t *testing.T) {
	c := compile(t, "Options -Indexes -MultiViews +FollowSymLinks\nOptions None")
	if probs := c.Problems(); len(probs) != 0 {
		t.Fatalf("%+v", probs)
	}
	var ignored bool
	for _, l := range c.Lines {
		ignored = ignored || (l.Status == Ignored && strings.Contains(l.Msg, "symlinks"))
	}
	if !ignored {
		t.Error("+FollowSymLinks must be reported as having no effect: it can't loosen the site's symlink policy")
	}
}

// ---- robustness ----

func TestEvaluationIsBoundedAndSafeOnOddInput(t *testing.T) {
	c := compileClean(t, "RewriteEngine On\nRewriteRule ^(.*)$ /$1/$1 [L]\nRewriteCond %{QUERY_STRING} (.*)\nRewriteRule .* /x?%1 [L]")
	for _, path := range []string{"/", "", "/" + strings.Repeat("a", 10000), "/%00", "/a\nb", "//", "/.."} {
		r := newReq(fakeFS{}, path, strings.Repeat("q", 5000))
		Evaluate(rootLayer(c), r, false) // must not panic
	}
}
