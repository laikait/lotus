package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/fastcgi/fcgitest"
	"github.com/laikait/lotus/internal/htaccess"
)

// htConfig is a .htaccess configuration with everything allowed and no caching.
func htConfig() *model.HTAccess {
	return &model.HTAccess{Allow: htaccess.Categories, OnUnsupported: model.HTAccessError, Recheck: 0}
}

// writeFiles creates files below root. Keys are slash paths.
func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		must(t, os.MkdirAll(filepath.Dir(p), 0o755))
		must(t, os.WriteFile(p, []byte(body), 0o644))
	}
}

// htServer starts a static site that reads .htaccess files.
func htServer(t *testing.T, files map[string]string, ht *model.HTAccess, mutate func(*model.Site)) (*harness, string, string) {
	t.Helper()
	cfg := routedConfig(t, func(s *model.Site) {
		s.HTAccess = ht
		writeFiles(t, s.Root, files)
		if mutate != nil {
			mutate(s)
		}
	}, nil)
	h := start(t, cfg)
	return h, "http://" + h.addr("http"), cfg.Sites[0].Root
}

func TestHtaccessIsOffUnlessEnabled(t *testing.T) {
	_, base, _ := htServer(t, map[string]string{
		".htaccess": "RewriteEngine On\nRewriteRule ^hello\\.txt$ /page.html [L]\nRequire all denied\n",
	}, nil, nil)
	if resp, body := do(t, "GET", base+"/hello.txt", nil, ""); resp.StatusCode != 200 || !strings.Contains(body, "hello") {
		t.Errorf("a site that didn't turn htaccess on must ignore the file: %d %q", resp.StatusCode, body)
	}
}

func TestHtaccessRewriteRedirectAndRefusal(t *testing.T) {
	_, base, _ := htServer(t, map[string]string{
		".htaccess": `RewriteEngine On
RewriteRule ^short$ /page.html [L]
RewriteRule ^moved/(.*)$ /new/$1 [R=301,L]
RewriteRule ^secret\.txt$ - [F]
RewriteRule ^old\.html$ - [G]
Redirect /legacy /page.html
`,
		"secret.txt": "s3cret",
	}, htConfig(), nil)
	for _, tc := range []struct {
		path   string
		status int
		loc    string
		body   string
	}{
		{"/short", 200, "", "page"},
		{"/moved/a%20b?x=1", 301, "/new/a%20b?x=1", ""},
		{"/secret.txt", 403, "", ""},
		{"/old.html", 410, "", ""},
		{"/legacy", 302, "/page.html", ""},
		{"/hello.txt", 200, "", "hello from home"},
	} {
		resp, body := do(t, "GET", base+tc.path, nil, "")
		if resp.StatusCode != tc.status || resp.Header.Get("Location") != tc.loc || (tc.body != "" && strings.TrimSpace(body) != tc.body) {
			t.Errorf("GET %s = %d Location %q body %q; want %d %q %q", tc.path, resp.StatusCode, resp.Header.Get("Location"), strings.TrimSpace(body), tc.status, tc.loc, tc.body)
		}
		if tc.path == "/secret.txt" && strings.Contains(body, "s3cret") {
			t.Error("a refused file's content was sent")
		}
	}
}

func TestHtaccessInSubdirectories(t *testing.T) {
	_, base, _ := htServer(t, map[string]string{
		".htaccess":             "RewriteEngine On\nRewriteRule ^blog/x$ /root-rule [L]\n",
		"blog/.htaccess":        "RewriteEngine On\nRewriteRule ^x$ y.txt [L]\n",
		"blog/y.txt":            "blog-y",
		"blog/sub/file.txt":     "sub-file",
		"closed/.htaccess":      "Require all denied\n",
		"closed/a.txt":          "closed",
		"closed/open/.htaccess": "Require all granted\n",
		"closed/open/b.txt":     "open",
	}, htConfig(), nil)
	// The child's rules replace the parent's, and a relative target gets the directory's URL.
	if resp, body := do(t, "GET", base+"/blog/x", nil, ""); resp.StatusCode != 200 || body != "blog-y" {
		t.Errorf("/blog/x = %d %q", resp.StatusCode, body)
	}
	if resp, body := do(t, "GET", base+"/blog/sub/file.txt", nil, ""); resp.StatusCode != 200 || body != "sub-file" {
		t.Errorf("a directory without its own file uses its parent's rules: %d %q", resp.StatusCode, body)
	}
	if resp, _ := do(t, "GET", base+"/closed/a.txt", nil, ""); resp.StatusCode != 403 {
		t.Errorf("/closed/a.txt = %d", resp.StatusCode)
	}
	if resp, body := do(t, "GET", base+"/closed/open/b.txt", nil, ""); resp.StatusCode != 200 || body != "open" {
		t.Errorf("a deeper rule that grants access replaces the parent's: %d %q", resp.StatusCode, body)
	}
	if resp, _ := do(t, "GET", base+"/hello.txt", nil, ""); resp.StatusCode != 200 {
		t.Errorf("a file outside the closed directory: %d", resp.StatusCode)
	}
}

func TestHtaccessFilesAndAccess(t *testing.T) {
	_, base, _ := htServer(t, map[string]string{
		".htaccess": `<FilesMatch "\.(sql|bak)$">
  Require all denied
</FilesMatch>
<Files "config.ini">
  Order allow,deny
  Deny from all
</Files>
`,
		"dump.sql": "SELECT 1", "config.ini": "pw=1", "ok.txt": "fine",
	}, htConfig(), nil)
	for path, want := range map[string]int{"/dump.sql": 403, "/config.ini": 403, "/ok.txt": 200} {
		if resp, _ := do(t, "GET", base+path, nil, ""); resp.StatusCode != want {
			t.Errorf("GET %s = %d, want %d", path, resp.StatusCode, want)
		}
	}
}

func TestHtaccessIPRules(t *testing.T) {
	_, base, _ := htServer(t, map[string]string{
		"a/.htaccess": "Require ip 10.0.0.0/8\n",
		"a/x.txt":     "x",
		"b/.htaccess": "<RequireAll>\nRequire all granted\nRequire not ip 192.0.2.0/24\n</RequireAll>\n",
		"b/x.txt":     "x",
	}, htConfig(), nil)
	// The test client is 127.0.0.1.
	if resp, _ := do(t, "GET", base+"/a/x.txt", nil, ""); resp.StatusCode != 403 {
		t.Errorf("not in the allowed range: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", base+"/b/x.txt", nil, ""); resp.StatusCode != 200 {
		t.Errorf("not in the denied range: %d", resp.StatusCode)
	}
}

func TestHtaccessErrorDocuments(t *testing.T) {
	_, base, _ := htServer(t, map[string]string{
		".htaccess": "ErrorDocument 404 /errors/404.html\nErrorDocument 403 \"No entry here\"\nErrorDocument 410 https://elsewhere.test/gone\n" +
			"RewriteEngine On\nRewriteRule ^forbidden$ - [F]\nRewriteRule ^gone$ - [G]\n",
		"errors/404.html": "custom-not-found",
	}, htConfig(), nil)
	resp, body := do(t, "GET", base+"/nothing-here", nil, "")
	if resp.StatusCode != 404 || strings.TrimSpace(body) != "custom-not-found" {
		t.Errorf("path document: %d %q (the status must stay 404)", resp.StatusCode, body)
	}
	resp, body = do(t, "GET", base+"/forbidden", nil, "")
	if resp.StatusCode != 403 || strings.TrimSpace(body) != "No entry here" {
		t.Errorf("text document: %d %q", resp.StatusCode, body)
	}
	resp, _ = do(t, "GET", base+"/gone", nil, "")
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "https://elsewhere.test/gone" {
		t.Errorf("URL document: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestHtaccessDirectoryIndex(t *testing.T) {
	_, base, _ := htServer(t, map[string]string{
		"a/.htaccess":  "DirectoryIndex start.txt index.html\n",
		"a/start.txt":  "start",
		"a/index.html": "index",
		"b/.htaccess":  "DirectoryIndex disabled\n",
		"b/index.html": "must-not-show",
	}, htConfig(), nil)
	if resp, body := do(t, "GET", base+"/a/", nil, ""); resp.StatusCode != 200 || body != "start" {
		t.Errorf("/a/ = %d %q", resp.StatusCode, body)
	}
	if resp, body := do(t, "GET", base+"/b/", nil, ""); resp.StatusCode != 403 || strings.Contains(body, "must-not-show") {
		t.Errorf("a disabled index: %d %q", resp.StatusCode, body)
	}
}

func TestHtaccessHeaders(t *testing.T) {
	_, base, _ := htServer(t, map[string]string{
		".htaccess": `Header set X-From-Htaccess yes
Header always set X-Frame-Options "ALLOWALL"
Header unset Server
Header append Vary "Accept"
Header set Cache-Control "max-age=60"
<Files "hello.txt">
Header set X-Only-Hello 1
</Files>
`,
	}, htConfig(), func(s *model.Site) {
		s.SecurityHeaders = []model.Header{{Name: "X-Frame-Options", Value: "DENY"}}
	})
	resp, _ := do(t, "GET", base+"/hello.txt", nil, "")
	h := resp.Header
	if h.Get("X-From-Htaccess") != "yes" || h.Get("X-Only-Hello") != "1" || h.Get("Cache-Control") != "max-age=60" {
		t.Errorf("headers: %v", h)
	}
	if got := h.Values("X-Frame-Options"); len(got) != 1 || got[0] != "DENY" {
		t.Errorf("X-Frame-Options = %v: a site file must not change the administrator's security headers", got)
	}
	if h.Get("Server") != "" {
		t.Errorf("Header unset Server: %q", h.Get("Server"))
	}
	if !strings.Contains(h.Get("Vary"), "Accept") {
		t.Errorf("Vary: %q", h.Get("Vary"))
	}
	if resp, _ := do(t, "GET", base+"/page.html", nil, ""); resp.Header.Get("X-Only-Hello") != "" {
		t.Error("a Files block's headers leaked to another file")
	}
}

func TestHtaccessFilesAreNeverServed(t *testing.T) {
	_, base, _ := htServer(t, map[string]string{
		".htaccess":     "Options -Indexes\n",
		"sub/.htaccess": "Options -Indexes\n",
		"sub/.htpasswd": "user:hash",
	}, htConfig(), func(s *model.Site) { s.Dotfiles = model.DotfilesAllow })
	for _, p := range []string{"/.htaccess", "/sub/.htaccess", "/sub/.htpasswd", "/.HTACCESS"} {
		resp, body := do(t, "GET", base+p, nil, "")
		if resp.StatusCode == 200 || strings.Contains(body, "Indexes") || strings.Contains(body, "hash") {
			t.Errorf("GET %s = %d %q: even with dotfiles allowed these are never served", p, resp.StatusCode, body)
		}
	}
}

func TestHtaccessCatchAllDoesNotTakeHiddenFilesForPages(t *testing.T) {
	p := htPHP(t, map[string]string{".htaccess": wpHtaccess, "sub/.htpasswd": "u:h"}, nil)
	for _, path := range []string{"/.htaccess", "/sub/.htpasswd", "/sub/.htaccess", "/.HTACCESS"} {
		resp, body := p.get(t, path)
		if resp.StatusCode != 403 || strings.Contains(body, "|") {
			t.Errorf("GET %s = %d %q: the catch-all rule must not run the application for it", path, resp.StatusCode, body)
		}
	}
	if p.calls.count() != 0 {
		t.Error("PHP ran for a hidden file")
	}
}

// ---- what is not supported ----

func TestHtaccessUnsupportedDirectiveFailsLoudly(t *testing.T) {
	h, base, _ := htServer(t, map[string]string{
		".htaccess": "ExpiresActive On\n",
	}, htConfig(), nil)
	resp, body := do(t, "GET", base+"/hello.txt", nil, "")
	if resp.StatusCode != 500 {
		t.Fatalf("status %d: an unsupported directive must not be silently skipped", resp.StatusCode)
	}
	if strings.Contains(body, "Expires") || strings.Contains(body, ".htaccess") {
		t.Errorf("the client was told about the file: %q", body)
	}
	if !strings.Contains(h.stderr.String(), "ExpiresActive") && !strings.Contains(h.stderr.String(), "expiresactive") {
		t.Errorf("the log must name the directive: %q", h.stderr.String())
	}
	if n := strings.Count(h.stderr.String(), "htaccess:"); n != 1 {
		t.Errorf("the same problem was logged %d times; once per file version is enough", n)
	}
}

func TestHtaccessIgnoreModeSkipsOnlyHarmlessDirectives(t *testing.T) {
	ht := htConfig()
	ht.OnUnsupported = model.HTAccessIgnore
	_, base, _ := htServer(t, map[string]string{
		"a/.htaccess": "ExpiresActive On\nphp_value memory_limit 256M\nHeader set X-Still-Works 1\n",
		"a/x.txt":     "x",
		"b/.htaccess": "ExpiresActive On\nAuthType Basic\nRequire valid-user\n",
		"b/x.txt":     "x",
		"c/.htaccess": "<FilesMatch \"^(?!x)\">\nRequire all denied\n</FilesMatch>\n",
		"c/x.txt":     "x",
	}, ht, nil)
	resp, _ := do(t, "GET", base+"/a/x.txt", nil, "")
	if resp.StatusCode != 200 || resp.Header.Get("X-Still-Works") != "1" {
		t.Errorf("harmless directives are skipped and the rest applies: %d %v", resp.StatusCode, resp.Header)
	}
	if resp, body := do(t, "GET", base+"/b/x.txt", nil, ""); resp.StatusCode != 500 || body == "x" {
		t.Errorf("a directive that restricts access must never be skipped: %d %q", resp.StatusCode, body)
	}
	if resp, body := do(t, "GET", base+"/c/x.txt", nil, ""); resp.StatusCode != 500 || body == "x" {
		t.Errorf("an access block that can't be compiled must not be skipped: %d %q", resp.StatusCode, body)
	}
}

func TestHtaccessAllowList(t *testing.T) {
	ht := &model.HTAccess{Allow: []string{htaccess.CatRewrite}, OnUnsupported: model.HTAccessError}
	h, base, _ := htServer(t, map[string]string{
		"a/.htaccess": "RewriteEngine On\nRewriteRule ^x$ y.txt [L]\n",
		"a/y.txt":     "y",
		"b/.htaccess": "Header set X-A 1\n",
		"b/x.txt":     "x",
		"c/.htaccess": "Require all denied\n",
		"c/x.txt":     "x",
	}, ht, nil)
	if resp, body := do(t, "GET", base+"/a/x", nil, ""); resp.StatusCode != 200 || body != "y" {
		t.Errorf("an allowed category: %d %q", resp.StatusCode, body)
	}
	for _, p := range []string{"/b/x.txt", "/c/x.txt"} {
		if resp, _ := do(t, "GET", base+p, nil, ""); resp.StatusCode != 500 {
			t.Errorf("GET %s = %d: a directive from a category the site doesn't allow must not be applied", p, resp.StatusCode)
		}
	}
	if !strings.Contains(h.stderr.String(), "does not allow") {
		t.Errorf("the log must say why: %q", h.stderr.String())
	}
}

func TestHtaccessRewriteLoopStopsAtTheLimit(t *testing.T) {
	h, base, _ := htServer(t, map[string]string{
		".htaccess": "RewriteEngine On\nRewriteRule ^(.*)$ /$1x [L]\n",
	}, htConfig(), nil)
	resp, _ := do(t, "GET", base+"/a", nil, "")
	if resp.StatusCode != 500 {
		t.Errorf("a rewrite loop: %d", resp.StatusCode)
	}
	if !strings.Contains(h.stderr.String(), "LOTUS-RT-0001") {
		t.Errorf("log: %q", h.stderr.String())
	}
}

// ---- change detection ----

func TestHtaccessChangesAreSeenAfterTheRecheckInterval(t *testing.T) {
	t.Run("recheck 0 sees a change at once", func(t *testing.T) {
		_, base, root := htServer(t, map[string]string{".htaccess": "Require all granted\n"}, htConfig(), nil)
		if resp, _ := do(t, "GET", base+"/hello.txt", nil, ""); resp.StatusCode != 200 {
			t.Fatal(resp.StatusCode)
		}
		must(t, os.WriteFile(filepath.Join(root, ".htaccess"), []byte("Require all denied\n"), 0o644))
		bumpMtime(t, filepath.Join(root, ".htaccess"))
		if resp, _ := do(t, "GET", base+"/hello.txt", nil, ""); resp.StatusCode != 403 {
			t.Errorf("after the edit: %d", resp.StatusCode)
		}
		must(t, os.Remove(filepath.Join(root, ".htaccess")))
		if resp, _ := do(t, "GET", base+"/hello.txt", nil, ""); resp.StatusCode != 200 {
			t.Errorf("after removing the file: %d", resp.StatusCode)
		}
	})
	t.Run("a long interval keeps the old file", func(t *testing.T) {
		ht := htConfig()
		ht.Recheck = model.Duration(time.Hour)
		_, base, root := htServer(t, map[string]string{".htaccess": "Require all granted\n"}, ht, nil)
		do(t, "GET", base+"/hello.txt", nil, "")
		must(t, os.WriteFile(filepath.Join(root, ".htaccess"), []byte("Require all denied\n"), 0o644))
		bumpMtime(t, filepath.Join(root, ".htaccess"))
		if resp, _ := do(t, "GET", base+"/hello.txt", nil, ""); resp.StatusCode != 200 {
			t.Errorf("the file system was read again within the interval: %d", resp.StatusCode)
		}
	})
}

// bumpMtime makes sure a rewritten file has a different modification time even
// on file systems with coarse timestamps.
func bumpMtime(t *testing.T, p string) {
	t.Helper()
	future := time.Now().Add(5 * time.Second)
	must(t, os.Chtimes(p, future, future))
}

// ---- with PHP ----

// htPHP starts a PHP site (fake backend) that reads .htaccess files.
func htPHP(t *testing.T, files map[string]string, mutate func(*model.Site)) *phpTest {
	t.Helper()
	return htPHPWith(t, nil, files, mutate)
}

// htPHPWith is htPHP with the fake script's behaviour (nil: echo).
func htPHPWith(t *testing.T, handler func(*fcgitest.Req), files map[string]string, mutate func(*model.Site)) *phpTest {
	t.Helper()
	re := `\.php(/|$)`
	routes := []model.Route{
		{Kind: model.RouteRegex, Path: re, Regexp: mustRe(re), Actions: model.Actions{PHP: "app"}},
		{Kind: model.RoutePrefix, Path: "/"}, // served from the file system; .htaccess decides the rest
	}
	return startPHP(t, handler, routes, func(s *model.Site) {
		s.HTAccess = htConfig()
		writeFiles(t, s.Root, files)
		if mutate != nil {
			mutate(s)
		}
	})
}

const wpHtaccess = `# BEGIN WordPress
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

func TestHtaccessWordPressPermalinks(t *testing.T) {
	p := htPHP(t, map[string]string{".htaccess": wpHtaccess}, nil)
	for path, want := range map[string]string{
		"/hello-world/":       "/index.php||",
		"/2024/05/a-post?p=3": "/index.php||p=3",
		"/":                   "/index.php||",
		"/app.php":            "/app.php||",
		"/index.php":          "/index.php||",
		"/info/":              "/info/index.php||",
		"/missing/page":       "/index.php||",
	} {
		resp, body := p.get(t, path)
		if resp.StatusCode != 200 || body != want {
			t.Errorf("GET %s = %d %q, want %q", path, resp.StatusCode, body, want)
		}
	}
	// Real files are not rewritten.
	before := p.calls.count()
	if resp, body := p.get(t, "/style.css"); resp.StatusCode != 200 || body != "body{}" || p.calls.count() != before {
		t.Errorf("a static file went to PHP: %d %q", resp.StatusCode, body)
	}
	params, _ := p.calls.last(t)
	if params["REQUEST_URI"] != "/missing/page" && params["SCRIPT_NAME"] == "/index.php" {
		// last call was the /missing/page request; REQUEST_URI must be the original URL
		t.Logf("REQUEST_URI = %q", params["REQUEST_URI"])
	}
}

func TestHtaccessRewriteKeepsTheOriginalRequestURI(t *testing.T) {
	p := htPHP(t, map[string]string{".htaccess": wpHtaccess}, nil)
	p.get(t, "/pretty/url?x=1")
	params, _ := p.calls.last(t)
	if params["REQUEST_URI"] != "/pretty/url?x=1" || params["SCRIPT_NAME"] != "/index.php" || params["QUERY_STRING"] != "x=1" {
		t.Errorf("the application must see the URL the client asked for: %v", params)
	}
}

func TestHtaccessLaravel(t *testing.T) {
	p := htPHP(t, map[string]string{".htaccess": `<IfModule mod_rewrite.c>
    <IfModule mod_negotiation.c>
        Options -MultiViews -Indexes
    </IfModule>
    RewriteEngine On
    RewriteCond %{HTTP:Authorization} .
    RewriteRule .* - [E=HTTP_AUTHORIZATION:%{HTTP:Authorization}]
    RewriteCond %{REQUEST_FILENAME} !-d
    RewriteCond %{REQUEST_URI} (.+)/$
    RewriteRule ^ %1 [L,R=301]
    RewriteCond %{REQUEST_FILENAME} !-d
    RewriteCond %{REQUEST_FILENAME} !-f
    RewriteRule ^ index.php [L]
    RewriteRule .* - [E=MY_FLAG:on]
</IfModule>
`}, nil)
	resp, _ := do(t, "GET", p.base+"/posts/", nil, "")
	if resp.StatusCode != 301 || resp.Header.Get("Location") != "/posts" {
		t.Errorf("trailing slash: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, body := p.get(t, "/posts"); resp.StatusCode != 200 || body != "/index.php||" {
		t.Errorf("front controller: %d %q", resp.StatusCode, body)
	}
}

func TestHtaccessEnvironmentVariablesReachPHPButCannotForgeServerOnes(t *testing.T) {
	p := htPHP(t, map[string]string{".htaccess": "RewriteEngine On\nRewriteRule .* - [E=MY_FLAG:on,E=SCRIPT_FILENAME:/etc/passwd,E=REMOTE_ADDR:6.6.6.6]\n"}, nil)
	p.get(t, "/app.php")
	params, _ := p.calls.last(t)
	if params["MY_FLAG"] != "on" {
		t.Errorf("E flag: %v", params["MY_FLAG"])
	}
	if params["SCRIPT_FILENAME"] != filepath.Join(p.root, "app.php") || params["REMOTE_ADDR"] != "127.0.0.1" {
		t.Errorf("a .htaccess file overrode what the server sets: %q %q", params["SCRIPT_FILENAME"], params["REMOTE_ADDR"])
	}
}

func TestHtaccessHeaderUnsetRemovesPHPHeaders(t *testing.T) {
	p := htPHPWith(t, scriptWithHeaders, htaccessHeaderFiles, nil)
	resp, _ := p.get(t, "/app.php")
	if resp.Header.Get("X-Powered-By") != "" {
		t.Errorf("Header unset must remove the script's header: %q", resp.Header.Get("X-Powered-By"))
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache, private" {
		t.Errorf("Header append adds to the script's value: %q", cc)
	}
	if resp.Header.Get("X-Added") != "1" {
		t.Error("Header set")
	}
}

func TestHtaccessDoesNotApplyToFixedReturns(t *testing.T) {
	cfg := routedConfig(t, func(s *model.Site) {
		s.HTAccess = htConfig()
		writeFiles(t, s.Root, map[string]string{".htaccess": "Require all denied\n"})
	}, []model.Route{
		{Kind: model.RouteExact, Path: "/ping", Actions: ret(200, "pong")},
	})
	h := start(t, cfg)
	base := "http://" + h.addr("http")
	if resp, body := do(t, "GET", base+"/ping", nil, ""); resp.StatusCode != 200 || strings.TrimSpace(body) != "pong" {
		t.Errorf("a `return` route doesn't read the file system: %d %q", resp.StatusCode, body)
	}
	if resp, _ := do(t, "GET", base+"/hello.txt", nil, ""); resp.StatusCode != 403 {
		t.Errorf("file requests are subject to it: %d", resp.StatusCode)
	}
}

var _ = http.StatusOK

const headerBlock = "Content-Type: text/plain\r\nX-Powered-By: PHP/8\r\nCache-Control: no-cache"

var htaccessHeaderFiles = map[string]string{
	".htaccess": "Header unset X-Powered-By\nHeader append Cache-Control \"private\"\nHeader set X-Added 1\n",
}

// The fake script sets both headers itself.
func scriptWithHeaders(r *fcgitest.Req) {
	r.Respond(headerBlock, "ok")
}
