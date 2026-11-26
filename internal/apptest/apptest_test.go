// Package apptest runs real PHP applications (WordPress, Laravel) behind the real LOTUS binary, to check that the
// site templates of the web admin work for them: installing, pretty URLs, uploads, the admin area, static files.
//
// These tests download the applications and need PHP and a MySQL server, so they are off unless told:
//
//	LOTUS_APPTEST_CACHE   a folder to keep the downloads in (WordPress zip, the Laravel project); turns the tests on
//	LOTUS_PHP_CGI         the php-cgi program                (default: php-cgi in PATH)
//	LOTUS_PHP             the php command line program       (default: php in PATH)
//	LOTUS_APPTEST_DB      host:port of a MySQL or MariaDB server that accepts user root without a password
//	                      (WordPress only; the test makes and drops its own database)
//	LOTUS_COMPOSER        the composer command               (Laravel only; default: composer in PATH)
//
// Run:  go test ./internal/apptest -run . -v -timeout 30m
package apptest

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/sitefile"
)

type harness struct {
	t      *testing.T
	cache  string
	phpCGI string
	php    string
	port   int
	client *http.Client
}

func setup(t *testing.T) *harness {
	t.Helper()
	cache := os.Getenv("LOTUS_APPTEST_CACHE")
	if cache == "" {
		t.Skip("set LOTUS_APPTEST_CACHE to run the application tests (see the package comment)")
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, cache: cache}
	h.phpCGI = tool(t, "LOTUS_PHP_CGI", "php-cgi")
	h.php = tool(t, "LOTUS_PHP", "php")
	return h
}

func tool(t *testing.T, env, name string) string {
	t.Helper()
	if v := os.Getenv(env); v != "" {
		return v
	}
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not found (set %s)", name, env)
	}
	return p
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// buildLotus builds the real binary under a name of its own.
func buildLotus(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	exe := filepath.Join(t.TempDir(), "lotus-apptest")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", exe, "./cmd/lotus")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building lotus: %v\n%s", err, out)
	}
	return exe
}

// start runs LOTUS with one site made through sitefile (so the template under test is the admin's own) and returns a
// client that sends every host name to it. The site is reached as http://<name>:<port>/.
func (h *harness) start(spec sitefile.Spec) string {
	t := h.t
	dir := t.TempDir()
	h.port = freePort(t)
	conf := filepath.Join(dir, "main.conf")
	text := fmt.Sprintf(`lotus_config_version 1

server {
    listen "http" {
        address "127.0.0.1:%d"
    }

    php_backend "app" {
        command %q
        processes 4
        max_requests 500
        timeout 120s
    }

    include "sites.d/*.conf"
}
`, h.port, filepath.ToSlash(h.phpCGI))
	if err := os.WriteFile(conf, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	spec.PHPBackend = "app"
	if _, err := (&sitefile.Store{Config: conf}).Add(spec, false); err != nil {
		t.Fatalf("making the site file: %v", err)
	}
	logf, err := os.Create(filepath.Join(dir, "lotus.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(buildLotus(t), "start", "-config", conf)
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		logf.Close()
		if t.Failed() {
			if data, err := os.ReadFile(filepath.Join(dir, "lotus.log")); err == nil {
				if len(data) > 4000 {
					data = data[len(data)-4000:]
				}
				t.Logf("lotus log (end):\n%s", data)
			}
		}
	})
	for i := 0; i < 100; i++ {
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", h.port), 200*time.Millisecond); err == nil {
			c.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	jar, _ := cookiejar.New(nil)
	h.client = &http.Client{
		Jar:     jar,
		Timeout: 120 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, fmt.Sprintf("127.0.0.1:%d", h.port))
		}},
	}
	return fmt.Sprintf("http://%s:%d", spec.Name, h.port)
}

type reply struct {
	Status int
	Header http.Header
	Body   string
	URL    string
}

func (h *harness) do(req *http.Request) reply {
	h.t.Helper()
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return reply{Status: resp.StatusCode, Header: resp.Header, Body: string(b), URL: resp.Request.URL.String()}
}

func (h *harness) get(u string) reply {
	h.t.Helper()
	req, _ := http.NewRequest("GET", u, nil)
	return h.do(req)
}

func (h *harness) post(u string, form url.Values) reply {
	h.t.Helper()
	req, _ := http.NewRequest("POST", u, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return h.do(req)
}

func (h *harness) expect(r reply, status int, contains string, what string) {
	h.t.Helper()
	if r.Status != status || (contains != "" && !strings.Contains(r.Body, contains)) {
		body := r.Body
		if len(body) > 600 {
			body = body[:600]
		}
		h.t.Errorf("%s: got %d (%s), want %d containing %q:\n%s", what, r.Status, r.URL, status, contains, body)
	}
}

// download keeps a file in the cache and returns its path.
func (h *harness) download(name, from string) string {
	t := h.t
	dest := filepath.Join(h.cache, name)
	if fi, err := os.Stat(dest); err == nil && fi.Size() > 0 {
		return dest
	}
	t.Logf("downloading %s", from)
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Get(from)
	if err != nil {
		t.Skipf("cannot download %s: %v", from, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Skipf("cannot download %s: %s", from, resp.Status)
	}
	f, err := os.Create(dest + ".part")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.Rename(dest+".part", dest); err != nil {
		t.Fatal(err)
	}
	return dest
}

func unzipTo(t *testing.T, zipPath, dest string) {
	t.Helper()
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		out := filepath.Join(dest, filepath.FromSlash(f.Name))
		if !strings.HasPrefix(out, filepath.Clean(dest)+string(filepath.Separator)) {
			t.Fatalf("unsafe path in %s", zipPath)
		}
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(out, 0o755)
			continue
		}
		_ = os.MkdirAll(filepath.Dir(out), 0o755)
		src, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		dst, err := os.Create(out)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(dst, src); err != nil {
			t.Fatal(err)
		}
		dst.Close()
		src.Close()
	}
}

// ---- WordPress ----

var nonceRe = regexp.MustCompile(`"_wpnonce":"([a-f0-9]+)"`)

func TestWordPress(t *testing.T) {
	h := setup(t)
	dbAddr := os.Getenv("LOTUS_APPTEST_DB")
	if dbAddr == "" {
		t.Skip("set LOTUS_APPTEST_DB (host:port of a MySQL server for user root without password)")
	}
	zipPath := h.download("wordpress-latest.zip", "https://wordpress.org/latest.zip")
	work := t.TempDir()
	unzipTo(t, zipPath, work)
	root := filepath.Join(work, "wordpress")

	// A database of its own, made and dropped with PHP's mysqli (no mysql client needed).
	dbName := fmt.Sprintf("lotus_apptest_%d", time.Now().UnixNano()%1e9)
	dbPHP := fmt.Sprintf(`<?php $c = new mysqli(%q, 'root', '', '', %d); if ($c->connect_error) { fwrite(STDERR, $c->connect_error); exit(1); } $c->query($argv[1] === 'drop' ? 'DROP DATABASE IF EXISTS %s' : 'CREATE DATABASE %s CHARACTER SET utf8mb4'); exit($c->errno ? 1 : 0);`,
		strings.Split(dbAddr, ":")[0], atoi(strings.Split(dbAddr, ":")[1]), dbName, dbName)
	script := filepath.Join(work, "db.php")
	if err := os.WriteFile(script, []byte(dbPHP), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(h.php, script, "create").CombinedOutput(); err != nil {
		t.Skipf("cannot reach the database %s: %v %s", dbAddr, err, out)
	}
	t.Cleanup(func() { _ = exec.Command(h.php, script, "drop").Run() })

	wpConfig := fmt.Sprintf(`<?php
define('DB_NAME', %q); define('DB_USER', 'root'); define('DB_PASSWORD', ''); define('DB_HOST', %q);
define('DB_CHARSET', 'utf8mb4'); define('DB_COLLATE', '');
define('AUTH_KEY','a'); define('SECURE_AUTH_KEY','b'); define('LOGGED_IN_KEY','c'); define('NONCE_KEY','d');
define('AUTH_SALT','e'); define('SECURE_AUTH_SALT','f'); define('LOGGED_IN_SALT','g'); define('NONCE_SALT','h');
$table_prefix = 'wp_'; define('WP_DEBUG', false); define('FS_METHOD', 'direct'); define('DISABLE_WP_CRON', true);
if ( ! defined('ABSPATH') ) { define('ABSPATH', __DIR__ . '/'); }
require_once ABSPATH . 'wp-settings.php';
`, dbName, dbAddr)
	if err := os.WriteFile(filepath.Join(root, "wp-config.php"), []byte(wpConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	base := h.start(sitefile.Spec{Name: "wp.test", Template: "wordpress", Root: filepath.ToSlash(root)})

	// 1. The installer runs.
	h.expect(h.get(base+"/"), 200, "WordPress", "the front page before the install redirects to the installer")
	r := h.get(base + "/wp-admin/install.php")
	h.expect(r, 200, "WordPress", "install.php")
	r = h.post(base+"/wp-admin/install.php?step=2", url.Values{
		"weblog_title": {"LOTUS Test Site"}, "user_name": {"admin"}, "admin_password": {"correct-horse-battery-1"},
		"admin_password2": {"correct-horse-battery-1"}, "pw_weak": {"1"}, "admin_email": {"admin@example.com"}, "blog_public": {"0"}, "Submit": {"Install WordPress"},
	})
	h.expect(r, 200, "installed", "the install step")

	// 2. The site works, with the default (ugly) permalinks.
	h.expect(h.get(base+"/"), 200, "LOTUS Test Site", "the front page")
	h.expect(h.get(base+"/?p=1"), 200, "Hello world!", "a post by id")
	h.expect(h.get(base+"/?rest_route=/"), 200, `"name":"LOTUS Test Site"`, "the REST API (plain permalinks use ?rest_route=)")

	// 3. The admin area: sign in, see the dashboard.
	h.expect(h.get(base+"/wp-login.php"), 200, "Log In", "the login page (it sets the test cookie WordPress checks)")
	r = h.post(base+"/wp-login.php", url.Values{"log": {"admin"}, "pwd": {"correct-horse-battery-1"}, "wp-submit": {"Log In"}, "redirect_to": {base + "/wp-admin/"}, "testcookie": {"1"}})
	h.expect(r, 200, "Dashboard", "signing in lands on the dashboard")

	// 4. Pretty permalinks: WordPress asks for them in the admin; the front controller (try_files) must serve them.
	r = h.get(base + "/wp-admin/options-permalink.php")
	h.expect(r, 200, "Permalink", "the permalink settings page")
	nonce := regexp.MustCompile(`name="_wpnonce" value="([a-f0-9]+)"`).FindStringSubmatch(r.Body)
	if nonce == nil {
		t.Fatalf("no nonce on the permalink page")
	}
	r = h.post(base+"/wp-admin/options-permalink.php", url.Values{"_wpnonce": {nonce[1]}, "permalink_structure": {"/%postname%/"}, "selection": {"/%postname%/"}, "submit": {"Save Changes"}})
	h.expect(r, 200, "", "saving the permalinks")
	h.expect(h.get(base+"/hello-world/"), 200, "Hello world!", "a pretty permalink (front controller)")
	h.expect(h.get(base+"/sample-page/"), 200, "Sample Page", "another pretty permalink")
	h.expect(h.get(base+"/no-such-page-at-all/"), 404, "", "a missing page is a 404")
	h.expect(h.get(base+"/feed/"), 200, "<rss", "the feed")
	h.expect(h.get(base+"/wp-json/"), 200, `"name":"LOTUS Test Site"`, "the REST API at /wp-json/ with pretty permalinks")

	// 5. Static files come straight from disk, with the right type.
	r = h.get(base + "/wp-includes/js/jquery/jquery.min.js")
	h.expect(r, 200, "jQuery", "jquery.min.js")
	if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("jquery.min.js content type %q", ct)
	}

	// 6. Uploading a file through the media screen, and fetching it.
	r = h.get(base + "/wp-admin/media-new.php")
	h.expect(r, 200, "", "the media upload page")
	up := nonceRe.FindStringSubmatch(r.Body)
	if up == nil {
		t.Fatalf("no upload nonce on the media page")
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("action", "upload-attachment")
	_ = mw.WriteField("_wpnonce", up[1])
	_ = mw.WriteField("name", "pixel.png")
	fw, _ := mw.CreateFormFile("async-upload", "pixel.png")
	fw.Write([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\xff\xff?\x00\x05\xfe\x02\xfe\xa7\x9a\x1e\xb7\x00\x00\x00\x00IEND\xaeB`\x82"))
	mw.Close()
	req, _ := http.NewRequest("POST", base+"/wp-admin/async-upload.php", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	r = h.do(req)
	h.expect(r, 200, `"success":true`, "the upload")
	if m := regexp.MustCompile(`"url":"([^"]+pixel[^"]*\.png)"`).FindStringSubmatch(r.Body); m != nil {
		u := strings.ReplaceAll(m[1], `\/`, "/")
		got := h.get(u)
		h.expect(got, 200, "PNG", "the uploaded image is served")
	} else {
		t.Errorf("no url in the upload answer: %.300s", r.Body)
	}

	// 7. Files in uploads are never run as PHP (the template denies it), and other files there are served.
	uploads := filepath.Join(root, "wp-content", "uploads")
	_ = os.MkdirAll(uploads, 0o755)
	_ = os.WriteFile(filepath.Join(uploads, "evil.php"), []byte(`<?php echo "EXECUTED";`), 0o644)
	_ = os.WriteFile(filepath.Join(uploads, "note.txt"), []byte("plain note"), 0o644)
	r = h.get(base + "/wp-content/uploads/evil.php")
	if r.Status == 200 || strings.Contains(r.Body, "EXECUTED") {
		t.Errorf("a PHP file in uploads was run or shown: %d %.100s", r.Status, r.Body)
	}
	h.expect(h.get(base+"/wp-content/uploads/note.txt"), 200, "plain note", "a text file in uploads")

	// 8. Files that must never be served.
	for _, p := range []string{"/wp-config.php", "/.htaccess", "/wp-config-sample.php"} {
		if r := h.get(base + p); r.Status == 200 && (strings.Contains(r.Body, "DB_PASSWORD") || strings.Contains(r.Body, "<?php")) {
			t.Errorf("%s leaked its source", p)
		}
	}
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// ---- Laravel ----

func TestLaravel(t *testing.T) {
	h := setup(t)
	composer := os.Getenv("LOTUS_COMPOSER")
	if composer == "" {
		p, err := exec.LookPath("composer")
		if err != nil {
			t.Skip("composer not found (set LOTUS_COMPOSER)")
		}
		composer = p
	}
	project := filepath.Join(h.cache, "laravel-project")
	if _, err := os.Stat(filepath.Join(project, "artisan")); err != nil {
		t.Log("creating a Laravel project with composer (a few minutes, kept in the cache)")
		cmd := exec.Command(composer, "create-project", "laravel/laravel", filepath.Base(project), "--no-interaction", "--prefer-dist", "--no-progress")
		cmd.Dir = h.cache
		cmd.Env = append(os.Environ(), "COMPOSER_HOME="+filepath.Join(h.cache, "composer-home"), "COMPOSER_CACHE_DIR="+filepath.Join(h.cache, "composer-cache"))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("composer create-project failed: %v\n%s", err, out)
		}
	}
	// The project is used in place (it is a cache); a fresh storage log keeps runs comparable.
	_ = os.RemoveAll(filepath.Join(project, "storage", "logs", "laravel.log"))

	base := h.start(sitefile.Spec{Name: "lv.test", Template: "laravel", Root: filepath.ToSlash(project)})

	h.expect(h.get(base+"/"), 200, "Laravel", "the welcome page")
	h.expect(h.get(base+"/up"), 200, "", "the health route /up")
	r := h.get(base + "/index.php/up")
	h.expect(r, 200, "", "PATH_INFO: /index.php/up")
	r = h.get(base + "/no/such/route")
	h.expect(r, 404, "", "an unknown URL goes to Laravel and is a 404")
	if !strings.Contains(r.Body, "404") && !strings.Contains(r.Body, "Not Found") {
		t.Errorf("the 404 should come from Laravel's own page: %.200s", r.Body)
	}
	// public/ is the root: files outside it are not reachable, and dotfiles are refused.
	for _, p := range []string{"/.env", "/artisan", "/composer.json", "/storage/logs/laravel.log", "/vendor/autoload.php", "/.git/config"} {
		rr := h.get(base + p)
		if rr.Status == 200 && !strings.Contains(rr.Body, "404") {
			t.Errorf("%s was served (%d): %.100s", p, rr.Status, rr.Body)
		}
	}
	// A static file of public/ is served straight from disk.
	h.expect(h.get(base+"/robots.txt"), 200, "User-agent", "robots.txt")
	// A POST goes to the front controller too: Laravel answers 419 (CSRF) or 405 for a route that has no POST.
	req, _ := http.NewRequest("POST", base+"/", strings.NewReader("a=b"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rr := h.do(req); rr.Status != 419 && rr.Status != 405 {
		t.Errorf("POST /: %d, want 419 or 405 from Laravel", rr.Status)
	}
}
