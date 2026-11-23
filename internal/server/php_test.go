package server

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/fastcgi/fcgitest"
)

// ---- helpers ----

// calls records what the fake PHP backend was asked to do.
type calls struct {
	mu    sync.Mutex
	list  []map[string]string
	stdin [][]byte
}

func (c *calls) add(p map[string]string, in []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.list = append(c.list, p)
	c.stdin = append(c.stdin, in)
}

func (c *calls) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.list)
}

func (c *calls) last(t *testing.T) (map[string]string, []byte) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.list) == 0 {
		t.Fatal("the PHP backend was never called")
	}
	return c.list[len(c.list)-1], c.stdin[len(c.stdin)-1]
}

// echo is the default fake script: it reports what it was run as.
func echo(r *fcgitest.Req) {
	r.Respond("Content-Type: text/plain", r.Params["SCRIPT_NAME"]+"|"+r.Params["PATH_INFO"]+"|"+r.Params["QUERY_STRING"])
}

// phpRoutes is the usual configuration: scripts run, uploads never do, and
// everything else falls back to the front controller.
func phpRoutes() []model.Route {
	re := `\.php(/|$)`
	return []model.Route{
		{Kind: model.RouteFinalPrefix, Path: "/uploads/", Actions: model.Actions{PHPDeny: true}},
		{Kind: model.RouteRegex, Path: re, Regexp: mustRe(re), Actions: model.Actions{PHP: "app"}},
		{Kind: model.RoutePrefix, Path: "/", Actions: model.Actions{TryFiles: []string{"{uri}", "{uri}/", "/index.php?{query}"}}},
	}
}

type phpTest struct {
	base  string
	root  string
	cfg   *model.Config
	h     *harness
	calls *calls
	be    *fcgitest.Backend
}

// writePHPFiles puts a small PHP application in root.
func writePHPFiles(t *testing.T, root string) {
	t.Helper()
	for name, body := range map[string]string{
		"index.php":           "<?php echo 'index';",
		"app.php":             "<?php echo 'app SECRET-SOURCE';",
		"info/index.php":      "<?php echo 'info';",
		"uploads/shell.php":   "<?php system($_GET['c']); // SECRET-SOURCE",
		"uploads/evil.jpg":    "<?php echo 'pwned'; ?>",
		"style.css":           "body{}",
		"data/notes.txt":      "notes",
		"data/index.html":     "data-index",
		"legacy/upload.phtml": "<?php // not run",
	} {
		must(t, os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755))
		must(t, os.WriteFile(filepath.Join(root, name), []byte(body), 0o644))
	}
}

// startPHP starts a server with a PHP backend. handler nil means echo.
func startPHP(t *testing.T, handler func(*fcgitest.Req), routes []model.Route, mutate func(*model.Site)) *phpTest {
	t.Helper()
	pt := &phpTest{calls: &calls{}}
	if handler == nil {
		handler = echo
	}
	pt.be = fcgitest.New(t, func(r *fcgitest.Req) {
		pt.calls.add(r.Params, nil) // tests that need the body record it themselves
		handler(r)
	})
	cfg := routedConfig(t, func(s *model.Site) {
		s.Index = []string{"index.php", "index.html"}
		writePHPFiles(t, s.Root)
		if mutate != nil {
			mutate(s)
		}
	}, routes)
	cfg.PHPBackends = []model.PHPBackend{{Name: "app", Network: "tcp", Address: pt.be.Addr(), Timeout: model.Duration(3 * time.Second)}}
	pt.cfg, pt.root = cfg, cfg.Sites[0].Root
	pt.h = start(t, cfg)
	pt.base = "http://" + pt.h.addr("http")
	return pt
}

func (p *phpTest) get(t *testing.T, path string) (*http.Response, string) {
	t.Helper()
	return do(t, "GET", p.base+path, nil, "")
}

// ---- unit tests ----

func TestSplitScript(t *testing.T) {
	for _, tc := range []struct {
		in, script, info string
		ok               bool
	}{
		{"/a.php", "/a.php", "", true},
		{"/a.php/b/c", "/a.php", "/b/c", true},
		{"/x/y.PHP/z", "/x/y.PHP", "/z", true},
		{"/a.php.txt/b.php", "/a.php.txt/b.php", "", true}, // only a real .php segment counts
		{"/a.php/b.php", "/a.php", "/b.php", true},         // the first one is the script
		{"/", "", "", false},
		{"/foo", "", "", false},
		{"/a.phpx/b", "", "", false},
		{"/a.php5", "", "", false},
		{"/dir.php/file.txt", "/dir.php", "/file.txt", true},
	} {
		script, info, ok := splitScript(tc.in)
		if script != tc.script || info != tc.info || ok != tc.ok {
			t.Errorf("splitScript(%q) = %q, %q, %v; want %q, %q, %v", tc.in, script, info, ok, tc.script, tc.info, tc.ok)
		}
	}
}

func TestPHPParamsFromARequest(t *testing.T) {
	r := httptest.NewRequest("POST", "/blog/index.php/a/b?x=1", strings.NewReader("data"))
	r.Host = "[::1]:8443"
	r.RemoteAddr = "192.0.2.7:5555"
	r.TLS = &tls.ConnectionState{}
	r.Header.Set("Content-Type", "text/plain")
	r.Header.Set("Content-Length", "4")
	r.Header.Set("X-Real-Thing", "v")
	r.Header.Add("Accept", "a/b")
	r.Header.Add("Accept", "c/d")
	r.Header.Add("Cookie", "a=1")
	r.Header.Add("Cookie", "b=2")
	r.Header.Set("Proxy", "http://evil.test:8080")
	r.Header["X_Forged"] = []string{"1"}
	r.Header["X-Fine_Name"] = []string{"1"}

	got := map[string]string{}
	for _, p := range phpParams(phpEnv{r: r, client: "198.51.100.9", root: "/srv/www", scriptName: "/blog/index.php", pathInfo: "/a/b", length: 4}) {
		if _, dup := got[p.Name]; dup {
			t.Errorf("parameter %s sent twice", p.Name)
		}
		got[p.Name] = p.Value
	}
	want := map[string]string{
		"GATEWAY_INTERFACE": "CGI/1.1", "SERVER_PROTOCOL": "HTTP/1.1", "SERVER_NAME": "::1",
		"REQUEST_SCHEME": "https", "HTTPS": "on", "REQUEST_METHOD": "POST",
		"REQUEST_URI": "/blog/index.php/a/b?x=1", "SCRIPT_NAME": "/blog/index.php",
		"SCRIPT_FILENAME": filepath.Join("/srv/www", "blog", "index.php"), "DOCUMENT_ROOT": "/srv/www",
		"PATH_INFO": "/a/b", "QUERY_STRING": "x=1", "REMOTE_ADDR": "198.51.100.9", "REMOTE_PORT": "5555",
		"CONTENT_TYPE": "text/plain", "CONTENT_LENGTH": "4", "HTTP_HOST": "[::1]:8443",
		"HTTP_X_REAL_THING": "v", "HTTP_ACCEPT": "a/b, c/d", "HTTP_COOKIE": "a=1; b=2", "REDIRECT_STATUS": "200",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	for _, bad := range []string{"HTTP_PROXY", "HTTP_X_FORGED", "HTTP_X_FINE_NAME", "HTTP_CONTENT_TYPE", "HTTP_CONTENT_LENGTH"} {
		if _, ok := got[bad]; ok {
			t.Errorf("%s must not be passed to PHP", bad)
		}
	}
}

func TestPHPParamsOmitWhatDoesNotApply(t *testing.T) {
	r := httptest.NewRequest("GET", "/a.php", nil)
	got := map[string]string{}
	for _, p := range phpParams(phpEnv{r: r, client: "1.2.3.4", root: "/r", scriptName: "/a.php"}) {
		got[p.Name] = p.Value
	}
	for _, k := range []string{"HTTPS", "PATH_INFO", "CONTENT_LENGTH", "CONTENT_TYPE"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s was set for a plain HTTP GET without a body", k)
		}
	}
	if got["REQUEST_SCHEME"] != "http" {
		t.Errorf("REQUEST_SCHEME = %q", got["REQUEST_SCHEME"])
	}
}

func TestSpoolBody(t *testing.T) {
	for _, size := range []int{0, 10, phpBodyMemory, phpBodyMemory + 1, 3 * phpBodyMemory} {
		data := bytes.Repeat([]byte("x"), size)
		r, n, cleanup, err := spoolBody(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(r)
		if n != int64(size) || !bytes.Equal(got, data) {
			t.Errorf("size %d: length %d, %d bytes read back", size, n, len(got))
		}
		var tmp string
		if f, ok := r.(*os.File); ok {
			tmp = f.Name()
			if size <= phpBodyMemory {
				t.Errorf("size %d should stay in memory", size)
			}
		}
		cleanup()
		if tmp != "" {
			if _, err := os.Stat(tmp); err == nil {
				t.Errorf("size %d: the temporary file was not removed", size)
			}
		}
	}
	// An error from the source is passed on.
	boom := io.MultiReader(strings.NewReader("a"), iotestErr{})
	if _, _, _, err := spoolBody(boom); err == nil {
		t.Error("a failing body was accepted")
	}
}

type iotestErr struct{}

func (iotestErr) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// ---- running scripts ----

func TestPHPRunsAScript(t *testing.T) {
	p := startPHP(t, nil, phpRoutes(), nil)
	resp, body := p.get(t, "/app.php?x=1&y=%20z")
	if resp.StatusCode != 200 || body != "/app.php||x=1&y=%20z" {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
	if resp.Header.Get("Content-Type") != "text/plain" || resp.Header.Get("Server") != "lotus" {
		t.Errorf("headers: %v", resp.Header)
	}
	params, _ := p.calls.last(t)
	for k, want := range map[string]string{
		"SCRIPT_FILENAME": filepath.Join(p.root, "app.php"),
		"DOCUMENT_ROOT":   p.root,
		"REQUEST_URI":     "/app.php?x=1&y=%20z",
		"REQUEST_METHOD":  "GET",
		"SERVER_PROTOCOL": "HTTP/1.1",
		"REMOTE_ADDR":     "127.0.0.1",
		"HTTP_HOST":       "a.test",
		"SERVER_NAME":     "a.test",
	} {
		if params[k] != want {
			t.Errorf("%s = %q, want %q", k, params[k], want)
		}
	}
	if params["SERVER_PORT"] == "" || params["SERVER_ADDR"] != "127.0.0.1" {
		t.Errorf("server address: %q %q", params["SERVER_ADDR"], params["SERVER_PORT"])
	}
	if _, ok := params["HTTPS"]; ok {
		t.Error("HTTPS set on a plain HTTP request")
	}
}

func TestPHPPathInfo(t *testing.T) {
	p := startPHP(t, nil, phpRoutes(), nil)
	_, body := p.get(t, "/app.php/a/b?c=d")
	if body != "/app.php|/a/b|c=d" {
		t.Errorf("%q", body)
	}
	// The first .php segment is the script, even if a later one exists too.
	_, body = p.get(t, "/app.php/other.php")
	if body != "/app.php|/other.php|" {
		t.Errorf("%q", body)
	}
}

func TestPHPDirectoryIndex(t *testing.T) {
	p := startPHP(t, nil, phpRoutes(), nil)
	for path, want := range map[string]string{
		"/":      "/index.php||",
		"/info/": "/info/index.php||",
		"/info":  "/info/index.php||", // try_files {uri}/
	} {
		resp, body := p.get(t, path)
		if resp.StatusCode != 200 || body != want {
			t.Errorf("GET %s = %d %q, want %q", path, resp.StatusCode, body, want)
		}
	}
	// A directory without an index.php keeps using its other index files.
	if _, body := p.get(t, "/data/"); body != "data-index" {
		t.Errorf("/data/ = %q", body)
	}
}

func TestPHPFrontController(t *testing.T) {
	p := startPHP(t, nil, phpRoutes(), nil)
	resp, body := p.get(t, "/pretty/url?x=1")
	if resp.StatusCode != 200 || body != "/index.php||x=1" {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
	params, _ := p.calls.last(t)
	if params["REQUEST_URI"] != "/pretty/url?x=1" {
		t.Errorf("REQUEST_URI = %q; the application needs the URL the client asked for", params["REQUEST_URI"])
	}
	// Static files are still served by the server itself.
	before := p.calls.count()
	if _, body := p.get(t, "/style.css"); body != "body{}" || p.calls.count() != before {
		t.Errorf("a static file went through PHP: %q", body)
	}
}

func TestPHPServesStaticFilesOnAPHPRoute(t *testing.T) {
	routes := []model.Route{{Kind: model.RoutePrefix, Path: "/", Actions: model.Actions{PHP: "app"}}}
	p := startPHP(t, nil, routes, nil)
	for path, want := range map[string]string{
		"/style.css":      "body{}",
		"/data/notes.txt": "notes",
		"/app.php":        "/app.php||",
		"/":               "/index.php||",
		"/app.php/x":      "/app.php|/x|",
	} {
		if resp, body := p.get(t, path); resp.StatusCode != 200 || body != want {
			t.Errorf("GET %s = %d %q, want %q", path, resp.StatusCode, body, want)
		}
	}
	before := p.calls.count()
	if resp, _ := p.get(t, "/nofile"); resp.StatusCode != 404 {
		t.Errorf("/nofile = %d", resp.StatusCode)
	}
	if p.calls.count() != before {
		t.Error("a missing file reached PHP")
	}
}

// ---- what must never run, or never be shown ----

func TestPHPOnlyRunsFilesThatExist(t *testing.T) {
	p := startPHP(t, nil, phpRoutes(), nil)
	for _, path := range []string{
		"/missing.php",
		"/uploads/evil.jpg/x.php", // an uploaded image is not a script path
		"/style.css/x.php",
		"/nodir/x.php",
		"/.hidden.php",
	} {
		before := p.calls.count()
		resp, body := p.get(t, path)
		if resp.StatusCode == 200 && strings.Contains(body, "|") {
			t.Errorf("GET %s ran a script: %q", path, body)
		}
		if p.calls.count() != before {
			t.Errorf("GET %s reached the PHP backend", path)
		}
	}
	if resp, _ := p.get(t, "/missing.php"); resp.StatusCode != 404 {
		t.Errorf("a missing script = %d, want 404", resp.StatusCode)
	}
	// Dot-dot is resolved before routing and can't leave the document root.
	if _, body := p.get(t, "/app.php/../../../../etc/passwd"); strings.Contains(body, "root:") {
		t.Errorf("escaped the document root: %q", body)
	}
}

func TestPHPDirectoryNamedLikeAScript(t *testing.T) {
	p := startPHP(t, nil, phpRoutes(), nil)
	must(t, os.MkdirAll(filepath.Join(p.root, "dir.php"), 0o755))
	must(t, os.WriteFile(filepath.Join(p.root, "dir.php", "index.php"), []byte("<?php"), 0o644))
	for _, path := range []string{"/dir.php", "/dir.php/", "/dir.php/x"} {
		resp, body := p.get(t, path)
		if resp.StatusCode == 200 && strings.Contains(body, "|") {
			t.Errorf("GET %s ran a script: %q", path, body)
		}
	}
	if p.calls.count() != 0 {
		t.Error("a directory was handed to PHP")
	}
}

func TestPHPNeverRunsOrShowsFilesWhenDenied(t *testing.T) {
	p := startPHP(t, nil, phpRoutes(), nil)
	resp, body := p.get(t, "/uploads/shell.php")
	if resp.StatusCode != 403 || strings.Contains(body, "SECRET-SOURCE") {
		t.Errorf("php_deny: %d %q", resp.StatusCode, body)
	}
	if p.calls.count() != 0 {
		t.Error("a php_deny route ran a script")
	}
	// Other files in the same directory are still ordinary static files.
	if resp, body := p.get(t, "/uploads/evil.jpg"); resp.StatusCode != 200 || !strings.Contains(body, "pwned") {
		t.Errorf("/uploads/evil.jpg = %d", resp.StatusCode)
	}
	// Upper-case spelling must not get around it (case-insensitive file systems).
	must(t, os.WriteFile(filepath.Join(p.root, "uploads", "Shell2.PHP"), []byte("SECRET-SOURCE"), 0o644))
	if resp, body := p.get(t, "/uploads/Shell2.PHP"); resp.StatusCode != 403 || strings.Contains(body, "SECRET") {
		t.Errorf("upper-case extension: %d %q", resp.StatusCode, body)
	}
}

func TestPHPSourceIsNotServedAsTextByRoutesWithoutPHP(t *testing.T) {
	// PHP runs under /app/ only. A .php file requested anywhere else must not
	// be handed out as text.
	routes := []model.Route{
		{Kind: model.RoutePrefix, Path: "/app/", Actions: model.Actions{PHP: "app"}},
		{Kind: model.RoutePrefix, Path: "/", Actions: model.Actions{TryFiles: []string{"{uri}", "=404"}}},
	}
	p := startPHP(t, nil, routes, nil)
	for _, path := range []string{"/app.php", "/uploads/shell.php", "/info/index.php"} {
		resp, body := p.get(t, path)
		if resp.StatusCode != 403 || strings.Contains(body, "<?php") || strings.Contains(body, "SECRET") {
			t.Errorf("GET %s = %d %q", path, resp.StatusCode, body)
		}
	}
	// A directory whose index is a PHP file, on a route that doesn't run PHP.
	if resp, body := p.get(t, "/info/"); resp.StatusCode != 403 || strings.Contains(body, "<?php") {
		t.Errorf("/info/ = %d %q", resp.StatusCode, body)
	}
	if p.calls.count() != 0 {
		t.Error("PHP ran on a route without the php action")
	}
}

func TestSitesWithoutPHPStillServeAnyFile(t *testing.T) {
	// No php action anywhere: the server doesn't know the file is special.
	root := docroot(t, "x")
	must(t, os.WriteFile(filepath.Join(root, "example.php"), []byte("<?php echo 1;"), 0o644))
	h := start(t, baseConfig(siteConf("a.test", root, "http")))
	resp, body := getHost(t, "http://"+h.addr("http")+"/example.php", "a.test")
	if resp.StatusCode != 200 || body != "<?php echo 1;" {
		t.Errorf("%d %q", resp.StatusCode, body)
	}
}

func TestPHPTraceIsRefused(t *testing.T) {
	p := startPHP(t, nil, phpRoutes(), nil)
	if resp, _ := do(t, "TRACE", p.base+"/app.php", nil, ""); resp.StatusCode != 405 {
		t.Errorf("TRACE = %d", resp.StatusCode)
	}
}

// ---- request bodies ----

func TestPHPRequestBody(t *testing.T) {
	handler := func(r *fcgitest.Req) {
		r.Respond("Content-Type: text/plain", fmt.Sprintf("%s|%s|%s", r.Params["CONTENT_LENGTH"], r.Params["CONTENT_TYPE"], r.Params["REQUEST_METHOD"]))
	}
	var got calls
	p := startPHP(t, func(r *fcgitest.Req) {
		got.add(r.Params, r.Stdin())
		handler(r)
	}, phpRoutes(), nil)

	resp, body := do(t, "POST", p.base+"/app.php", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, "a=1&b=2")
	if resp.StatusCode != 200 || body != "7|application/x-www-form-urlencoded|POST" {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
	if _, in := got.last(t); string(in) != "a=1&b=2" {
		t.Errorf("the script received %q", in)
	}
}

func TestPHPChunkedRequestBodyGetsAContentLength(t *testing.T) {
	var got calls
	p := startPHP(t, func(r *fcgitest.Req) {
		got.add(r.Params, r.Stdin())
		r.Respond("", r.Params["CONTENT_LENGTH"])
	}, phpRoutes(), nil)

	for _, size := range []int{5, 3<<20 + 17} { // the second one is spooled to a file
		payload := bytes.Repeat([]byte("abcdefg"), size/7+1)[:size]
		// A reader of unknown length makes the client send chunked.
		req, _ := http.NewRequest("POST", p.base+"/app.php", io.NopCloser(bytes.NewReader(payload)))
		req.Host = "a.test"
		resp, err := http.DefaultClient.Do(req)
		must(t, err)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(b) != fmt.Sprint(size) {
			t.Errorf("size %d: CONTENT_LENGTH was %q; PHP can't read a body without it", size, b)
		}
		if _, in := got.last(t); !bytes.Equal(in, payload) {
			t.Errorf("size %d: the script received %d bytes", size, len(in))
		}
	}
}

func TestPHPBodyLimit(t *testing.T) {
	p := startPHP(t, nil, phpRoutes(), func(s *model.Site) { s.Limits.MaxRequestBody = 1024 })
	big := strings.Repeat("x", 5000)

	resp, _ := do(t, "POST", p.base+"/app.php", nil, big) // declared length
	if resp.StatusCode != 413 {
		t.Errorf("declared length: %d, want 413", resp.StatusCode)
	}
	req, _ := http.NewRequest("POST", p.base+"/app.php", io.NopCloser(strings.NewReader(big))) // chunked
	req.Host = "a.test"
	r2, err := http.DefaultClient.Do(req)
	must(t, err)
	r2.Body.Close()
	if r2.StatusCode != 413 {
		t.Errorf("chunked: %d, want 413", r2.StatusCode)
	}
	if p.calls.count() != 0 {
		t.Error("an oversized request reached PHP")
	}
}

func TestPHPRequestHeaders(t *testing.T) {
	p := startPHP(t, nil, phpRoutes(), nil)
	do(t, "GET", p.base+"/app.php", map[string]string{
		"X-Custom":      "a",
		"Proxy":         "http://evil.test:3128",
		"X_Underscore":  "forged",
		"Authorization": "Bearer t",
		"Cookie":        "s=1",
	}, "")
	params, _ := p.calls.last(t)
	if params["HTTP_X_CUSTOM"] != "a" || params["HTTP_AUTHORIZATION"] != "Bearer t" || params["HTTP_COOKIE"] != "s=1" {
		t.Errorf("headers were not passed on: %v", params)
	}
	if _, ok := params["HTTP_PROXY"]; ok {
		t.Error("HTTP_PROXY reached PHP (httpoxy)")
	}
	if _, ok := params["HTTP_X_UNDERSCORE"]; ok {
		t.Error("a header with an underscore reached PHP")
	}
}

func TestPHPIgnoresForwardedForFromUntrustedClients(t *testing.T) {
	p := startPHP(t, nil, phpRoutes(), nil)
	// Without a trusted proxy, X-Forwarded-For is ignored.
	do(t, "GET", p.base+"/app.php", map[string]string{"X-Forwarded-For": "203.0.113.5"}, "")
	if params, _ := p.calls.last(t); params["REMOTE_ADDR"] != "127.0.0.1" {
		t.Errorf("untrusted X-Forwarded-For was believed: %q", params["REMOTE_ADDR"])
	}
}

// ---- responses ----

func TestPHPResponseHeaders(t *testing.T) {
	p := startPHP(t, func(r *fcgitest.Req) {
		r.Respond("Status: 201 Created\r\n"+
			"Set-Cookie: a=1\r\nSet-Cookie: b=2\r\n"+
			"X-Accel-Redirect: /secret\r\nX-Sendfile: /etc/passwd\r\n"+
			"Server: Apache\r\nConnection: close\r\nKeep-Alive: timeout=5\r\n"+
			"X-Frame-Options: ALLOWALL\r\n"+
			"X-Powered-By: PHP/8\r\nContent-Type: text/html", "made")
	}, phpRoutes(), func(s *model.Site) {
		s.SecurityHeaders = []model.Header{{Name: "X-Frame-Options", Value: "DENY"}}
	})
	resp, body := p.get(t, "/app.php")
	if resp.StatusCode != 201 || body != "made" {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
	if c := resp.Header.Values("Set-Cookie"); len(c) != 2 {
		t.Errorf("Set-Cookie: %v (each cookie must arrive)", c)
	}
	for _, h := range []string{"X-Accel-Redirect", "X-Sendfile", "Keep-Alive"} {
		if resp.Header.Get(h) != "" {
			t.Errorf("%s was passed on", h)
		}
	}
	if resp.Header.Get("Server") != "lotus" {
		t.Errorf("Server = %q", resp.Header.Get("Server"))
	}
	if resp.Header.Get("X-Powered-By") != "PHP/8" {
		t.Error("ordinary headers must pass")
	}
	if got := resp.Header.Values("X-Frame-Options"); len(got) != 1 || got[0] != "DENY" {
		t.Errorf("X-Frame-Options = %q: a script must not override or add to the administrator's security headers", got)
	}
}

func TestPHPLocationAlone(t *testing.T) {
	p := startPHP(t, func(r *fcgitest.Req) { r.Respond("Location: /elsewhere", "") }, phpRoutes(), nil)
	resp, _ := p.get(t, "/app.php")
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "/elsewhere" {
		t.Errorf("%d %v", resp.StatusCode, resp.Header)
	}
}

func TestPHPHeadRequest(t *testing.T) {
	p := startPHP(t, func(r *fcgitest.Req) { r.Respond("Content-Type: text/plain", "body") }, phpRoutes(), nil)
	resp, body := do(t, "HEAD", p.base+"/app.php", nil, "")
	if resp.StatusCode != 200 || body != "" {
		t.Errorf("%d %q", resp.StatusCode, body)
	}
}

func TestPHPResponsesAreNotReplacedByErrorPages(t *testing.T) {
	p := startPHP(t, func(r *fcgitest.Req) {
		if r.Params["SCRIPT_NAME"] == "/app.php" {
			r.Respond("Status: 404 Not Found", "the application's own 404")
			return
		}
		echo(r)
	}, phpRoutes(), func(s *model.Site) { s.ErrorPages = map[int]string{404: "/404.html"} })

	// The application's 404 is its own page.
	resp, body := p.get(t, "/app.php")
	if resp.StatusCode != 404 || body != "the application's own 404" {
		t.Errorf("script 404: %d %q", resp.StatusCode, body)
	}
	// The server's own 404 (no such script) uses the configured page.
	resp, body = p.get(t, "/gone.php")
	if resp.StatusCode != 404 || body != "custom-404" {
		t.Errorf("server 404: %d %q", resp.StatusCode, body)
	}
}

func TestPHPStreamsItsOutput(t *testing.T) {
	proceed := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-proceed:
		default:
			close(proceed)
		}
	})
	p := startPHP(t, func(r *fcgitest.Req) {
		r.Stdin()
		r.Stdout("Content-Type: text/plain\r\n\r\nfirst-")
		<-proceed
		r.Stdout("second")
		r.End(0, 0)
	}, phpRoutes(), nil)

	req, _ := http.NewRequest("GET", p.base+"/app.php", nil)
	req.Host = "a.test"
	resp, err := http.DefaultClient.Do(req)
	must(t, err)
	defer resp.Body.Close()
	buf := make([]byte, 6)
	done := make(chan error, 1)
	go func() { _, err := io.ReadFull(resp.Body, buf); done <- err }()
	select {
	case err := <-done:
		if err != nil || string(buf) != "first-" {
			t.Fatalf("%v %q", err, buf)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("output was held back until the script finished")
	}
	close(proceed)
	rest, _ := io.ReadAll(resp.Body)
	if string(rest) != "second" {
		t.Errorf("rest %q", rest)
	}
}

// ---- failures ----

func TestPHPBackendDown(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	cfg := routedConfig(t, func(s *model.Site) { writePHPFiles(t, s.Root) }, phpRoutes())
	cfg.PHPBackends = []model.PHPBackend{{Name: "app", Network: "tcp", Address: addr, Timeout: model.Duration(time.Second)}}
	h := start(t, cfg)
	resp, body := do(t, "GET", "http://"+h.addr("http")+"/app.php", nil, "")
	if resp.StatusCode != 502 || strings.Contains(body, addr) {
		t.Errorf("%d %q (the backend's address must not be revealed)", resp.StatusCode, body)
	}
	if !strings.Contains(h.stderr.String(), "php") {
		t.Errorf("nothing was logged: %q", h.stderr.String())
	}
}

func TestPHPCrashIsAnErrorWithTheReasonInTheLog(t *testing.T) {
	p := startPHP(t, func(r *fcgitest.Req) {
		r.Stdin()
		r.Stderr("PHP Fatal error: Uncaught Error in /srv/secret/path/app.php:3")
		r.End(255, 0)
	}, phpRoutes(), nil)
	resp, body := p.get(t, "/app.php")
	if resp.StatusCode != 502 {
		t.Errorf("status %d", resp.StatusCode)
	}
	if strings.Contains(body, "Fatal") || strings.Contains(body, "secret") {
		t.Errorf("the client was shown PHP's error output: %q", body)
	}
	if !strings.Contains(p.h.stderr.String(), "Uncaught Error") {
		t.Errorf("the error log does not say why: %q", p.h.stderr.String())
	}
}

func TestPHPTimeout(t *testing.T) {
	release := make(chan struct{})
	defer close(release) // before the backend's own cleanup, which waits for its handlers
	p := startPHP(t, func(r *fcgitest.Req) { r.Stdin(); <-release }, phpRoutes(), nil)
	p.cfg.PHPBackends[0].Timeout = model.Duration(300 * time.Millisecond)
	must(t, p.h.srv.Reload(p.cfg))
	start := time.Now()
	resp, _ := p.get(t, "/app.php")
	if resp.StatusCode != 504 {
		t.Errorf("status %d, want 504", resp.StatusCode)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v", d)
	}
}

func TestPHPTruncatedResponseIsNotPassedOffAsComplete(t *testing.T) {
	p := startPHP(t, func(r *fcgitest.Req) {
		r.Stdin()
		r.Stdout("Content-Type: text/plain\r\n\r\n" + strings.Repeat("a", 5000))
		r.Close() // dies mid-response
	}, phpRoutes(), nil)
	resp, err := http.Get(p.base + "/app.php")
	if err == nil {
		defer resp.Body.Close()
		_, err = io.ReadAll(resp.Body)
	}
	if err == nil {
		t.Error("the client saw a clean end of a response that was cut short")
	}
}

func TestPHPKeepsWorkingAfterAReload(t *testing.T) {
	p := startPHP(t, nil, phpRoutes(), nil)
	if _, body := p.get(t, "/app.php"); body != "/app.php||" {
		t.Fatal(body)
	}
	for i := 0; i < 3; i++ {
		must(t, p.h.srv.Reload(p.cfg))
		if _, body := p.get(t, "/app.php"); body != "/app.php||" {
			t.Fatalf("after reload %d: %q", i+1, body)
		}
	}
}

func TestPHPConcurrentRequests(t *testing.T) {
	p := startPHP(t, nil, phpRoutes(), nil)
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				q := fmt.Sprintf("n=%d-%d", i, j)
				resp, body := do(t, "GET", p.base+"/app.php?"+q, nil, "")
				if resp.StatusCode != 200 || body != "/app.php||"+q {
					t.Errorf("%d %q", resp.StatusCode, body)
					return
				}
			}
		}()
	}
	wg.Wait()
}
