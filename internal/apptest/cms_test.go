package apptest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/sitefile"
)

// database makes a database of its own on the server in LOTUS_APPTEST_DB and drops it when the test ends. It uses
// PHP's mysqli, so no mysql client is needed.
func (h *harness) database(work string) (name, host string, port int) {
	t := h.t
	dbAddr := os.Getenv("LOTUS_APPTEST_DB")
	if dbAddr == "" {
		t.Skip("set LOTUS_APPTEST_DB (host:port of a MySQL server for user root without password)")
	}
	host, port = strings.Split(dbAddr, ":")[0], atoi(strings.Split(dbAddr, ":")[1])
	name = fmt.Sprintf("lotus_apptest_%d", time.Now().UnixNano()%1e9)
	script := filepath.Join(work, "db.php")
	body := fmt.Sprintf(`<?php $c = new mysqli(%q, 'root', '', '', %d); if ($c->connect_error) { fwrite(STDERR, $c->connect_error); exit(1); } $c->query($argv[1] === 'drop' ? 'DROP DATABASE IF EXISTS %s' : 'CREATE DATABASE %s CHARACTER SET utf8mb4'); exit($c->errno ? 1 : 0);`, host, port, name, name)
	if err := os.WriteFile(script, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(h.php, script, "create").CombinedOutput(); err != nil {
		t.Skipf("cannot reach the database %s: %v %s", dbAddr, err, out)
	}
	t.Cleanup(func() { _ = exec.Command(h.php, script, "drop").Run() })
	return name, host, port
}

// run runs a PHP command line program (the application's installer) and fails the test if it fails.
func (h *harness) runPHP(dir string, args ...string) string {
	h.t.Helper()
	cmd := exec.Command(h.php, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		h.t.Fatalf("php %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// ---- Joomla ----

// joomlaRelease finds the newest stable release of a series ("5") on GitHub, and the URL of its full package.
func joomlaRelease(t *testing.T, series string) (tag, from string) {
	t.Helper()
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Get("https://api.github.com/repos/joomla/joomla-cms/releases?per_page=60")
	if err != nil {
		t.Skipf("cannot list the Joomla releases: %v", err)
	}
	defer resp.Body.Close()
	var rel []struct {
		Tag        string `json:"tag_name"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil || resp.StatusCode != 200 {
		t.Skipf("cannot read the Joomla releases: %v (%s)", err, resp.Status)
	}
	for _, r := range rel { // newest first
		if r.Prerelease || !strings.HasPrefix(r.Tag, series+".") {
			continue
		}
		for _, a := range r.Assets {
			if strings.HasSuffix(a.Name, "-Stable-Full_Package.zip") {
				return r.Tag, a.URL
			}
		}
	}
	t.Skipf("no stable Joomla %s release found", series)
	return "", ""
}

var joomlaTokenRe = regexp.MustCompile(`name="([a-f0-9]{32})" value="1"`)

func TestJoomla(t *testing.T) {
	h := setup(t)
	series := os.Getenv("LOTUS_JOOMLA_SERIES") // Joomla 6 needs PHP 8.3; 5 runs on 8.1 and later
	if series == "" {
		series = "5"
	}
	tag, from := joomlaRelease(t, series)
	zipPath := h.download("joomla-"+tag+".zip", from)
	work := t.TempDir()
	root := filepath.Join(work, "joomla")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	unzipTo(t, zipPath, root)
	db, host, port := h.database(work)

	h.runPHP(root, "installation/joomla.php", "install",
		"--site-name=LOTUS Joomla", "--admin-user=Administrator", "--admin-username=admin", "--admin-password=correct-horse-battery-1",
		"--admin-email=admin@example.com", "--db-type=mysqli", fmt.Sprintf("--db-host=%s:%d", host, port), "--db-user=root", "--db-pass=pw",
		"--db-name="+db, "--db-prefix=lt_", "--db-encryption=0", "--no-interaction")
	if err := os.RemoveAll(filepath.Join(root, "installation")); err != nil {
		t.Fatal(err)
	}
	// Search-engine friendly URLs without index.php: the setting that needs the template's front controller.
	cfgPath := filepath.Join(root, "configuration.php")
	cfg, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "public $sef_rewrite = false;") {
		t.Fatalf("configuration.php has no sef_rewrite setting")
	}
	if err := os.WriteFile(cfgPath, []byte(strings.Replace(string(cfg), "public $sef_rewrite = false;", "public $sef_rewrite = true;", 1)), 0o644); err != nil {
		t.Fatal(err)
	}

	base := h.start(sitefile.Spec{Name: "jm.test", Template: "joomla", Root: filepath.ToSlash(root)})

	h.expect(h.get(base+"/"), 200, "LOTUS Joomla", "the front page")
	h.expect(h.get(base+"/index.php"), 200, "LOTUS Joomla", "index.php itself")
	h.expect(h.get(base+"/index.php?option=com_users&view=login"), 200, "login", "a component URL with a query string")
	// With rewriting on, URLs have no index.php: they are not files, so the front controller must get them.
	r := h.get(base + "/component/users/?view=login")
	h.expect(r, 200, "login", "a URL without index.php (rewriting)")
	h.expect(h.get(base+"/index.php/component/users/?view=login"), 200, "login", "PATH_INFO: /index.php/...")
	r = h.get(base + "/no-such-page-at-all")
	h.expect(r, 404, "", "a missing page is a 404")

	// The administrator: sign in.
	r = h.get(base + "/administrator/")
	h.expect(r, 200, "Log in", "the administrator login page")
	tok := joomlaTokenRe.FindStringSubmatch(r.Body)
	if tok == nil {
		t.Fatalf("no form token on the login page: %.400s", r.Body)
	}
	r = h.post(base+"/administrator/index.php", url.Values{"username": {"admin"}, "passwd": {"correct-horse-battery-1"}, "option": {"com_login"}, "task": {"login"}, tok[1]: {"1"}})
	if r.Status != 200 || (!strings.Contains(r.Body, "Control Panel") && !strings.Contains(r.Body, "Log out")) {
		t.Errorf("signing in: %d %s: %.300s", r.Status, r.URL, r.Body)
	}
	h.expect(h.get(base+"/administrator/index.php?option=com_config"), 200, "Global Configuration", "an administrator page")

	// Files and folders.
	rr := h.get(base + "/media/system/css/joomla-fontawesome.min.css")
	if rr.Status != 200 || !strings.Contains(rr.Header.Get("Content-Type"), "css") {
		t.Errorf("a static stylesheet: %d %s", rr.Status, rr.Header.Get("Content-Type"))
	}
	_ = os.WriteFile(filepath.Join(root, "images", "x.php"), []byte("<?php echo 'RAN';"), 0o644)
	if rr := h.get(base + "/images/x.php"); rr.Status == 200 || strings.Contains(rr.Body, "RAN") {
		t.Errorf("a PHP file in images ran: %d %.100s", rr.Status, rr.Body)
	}
	if rr := h.get(base + "/configuration.php"); strings.Contains(rr.Body, "dbtype") || strings.Contains(rr.Body, "secret") {
		t.Errorf("configuration.php leaked: %.200s", rr.Body)
	}
	if rr := h.get(base + "/.git/config"); rr.Status == 200 {
		t.Errorf(".git/config was served")
	}
}

// ---- Drupal ----

func TestDrupal(t *testing.T) {
	h := setup(t)
	project := filepath.Join(h.cache, "drupal-project")
	if _, err := os.Stat(filepath.Join(project, "web", "index.php")); err != nil {
		t.Log("creating a Drupal project with composer (a few minutes, kept in the cache)")
		version := os.Getenv("LOTUS_DRUPAL_VERSION") // Drupal 11 needs PHP 8.3; 10 runs on 8.1 and later
		if version == "" {
			version = "^10"
		}
		h.composer(h.cache, "create-project", "drupal/recommended-project:"+version, filepath.Base(project), "--no-interaction", "--prefer-dist", "--no-progress")
	}
	if _, err := os.Stat(filepath.Join(project, "vendor", "drush", "drush", "drush")); err != nil {
		h.composer(project, "require", "drush/drush", "--no-interaction", "--no-progress")
	}
	web := filepath.Join(project, "web")
	if _, err := os.Stat(filepath.Join(web, "sites", "default", "settings.php")); err != nil {
		t.Log("installing Drupal (SQLite, kept in the cache)")
		_ = os.RemoveAll(filepath.Join(web, "sites", "default", "files"))
		_ = os.MkdirAll(filepath.Join(web, "sites", "default", "files"), 0o755)
		h.runPHP(project, "vendor/drush/drush/drush", "site:install", "standard", "--db-url=sqlite://sites/default/files/.ht.sqlite",
			"--account-name=admin", "--account-pass=correct-horse-battery-1", "--site-name=LOTUS Drupal", "--yes")
	}

	base := h.start(sitefile.Spec{Name: "dr.test", Template: "drupal", Root: filepath.ToSlash(web)})

	r := h.get(base + "/")
	h.expect(r, 200, "LOTUS Drupal", "the front page")
	// A stylesheet the page links to is served from disk with the right type.
	if m := regexp.MustCompile(`href="(/[^"]+\.css[^"]*)"`).FindStringSubmatch(r.Body); m == nil {
		t.Error("the front page links no stylesheet")
	} else if rr := h.get(base + strings.ReplaceAll(m[1], "&amp;", "&")); rr.Status != 200 || !strings.Contains(rr.Header.Get("Content-Type"), "css") {
		t.Errorf("stylesheet %s: %d %s", m[1], rr.Status, rr.Header.Get("Content-Type"))
	}
	h.expect(h.get(base+"/index.php/user/login"), 200, "Log in", "PATH_INFO: /index.php/user/login")
	r = h.get(base + "/user/login")
	h.expect(r, 200, "user_login_form", "a clean URL (front controller)")
	h.expect(h.get(base+"/no/such/page"), 404, "Page not found", "a missing page is Drupal's own 404")

	// Sign in.
	form := regexp.MustCompile(`name="form_build_id" value="([^"]+)"`).FindStringSubmatch(r.Body)
	if form == nil {
		t.Fatalf("no form_build_id on the login page")
	}
	r = h.post(base+"/user/login", url.Values{"name": {"admin"}, "pass": {"correct-horse-battery-1"}, "form_build_id": {form[1]}, "form_id": {"user_login_form"}, "op": {"Log in"}})
	if r.Status != 200 || !strings.Contains(r.URL, "/user/1") {
		t.Errorf("signing in: %d %s: %.300s", r.Status, r.URL, r.Body)
	}
	h.expect(h.get(base+"/admin/reports/status"), 200, "Status report", "an administration page")
	h.expect(h.get(base+"/node/add"), 200, "Add content", "another administration page")

	// The project folder is above the web root; sensitive files in it are unreachable or blocked.
	for _, p := range []string{"/composer.json", "/vendor/autoload.php", "/.git/config", "/sites/default/files/.htaccess", "/sites/default/files/.ht.sqlite"} {
		rr := h.get(base + p)
		if rr.Status == 200 {
			t.Errorf("%s was served (%d): %.100s", p, rr.Status, rr.Body)
		}
	}
	if rr := h.get(base + "/sites/default/settings.php"); strings.Contains(rr.Body, "databases") || strings.Contains(rr.Body, "hash_salt") {
		t.Errorf("settings.php leaked: %.200s", rr.Body)
	}
	_ = os.WriteFile(filepath.Join(web, "sites", "default", "files", "x.php"), []byte("<?php echo 'RAN';"), 0o644)
	if rr := h.get(base + "/sites/default/files/x.php"); rr.Status == 200 || strings.Contains(rr.Body, "RAN") {
		t.Errorf("a PHP file in sites/default/files ran: %d %.100s", rr.Status, rr.Body)
	}
}
