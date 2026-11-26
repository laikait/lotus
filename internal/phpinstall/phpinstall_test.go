package phpinstall

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

const sampleIni = "[PHP]\r\n;   extension=curl\r\n;   memory_limit = 1G\r\n; extension_dir = \"ext\"\r\nmemory_limit = 128M\r\n;date.timezone =\r\n;extension=curl\r\n;extension=mbstring\r\n;extension=gd\r\nupload_max_filesize = 2M\r\n[opcache]\r\n;opcache.enable=1\r\n;opcache.enable_cli=0\r\n"

// buildZip makes a small stand-in for an official PHP zip.
func buildZip(t *testing.T, extra map[string]string) []byte {
	t.Helper()
	files := map[string]string{
		"php-cgi.exe":          "stand-in",
		"php.ini-production":   sampleIni,
		"ext/php_curl.dll":     "x",
		"ext/php_mbstring.dll": "x",
		"ext/php_opcache.dll":  "x",
	}
	for k, v := range extra {
		if v == "<delete>" {
			delete(files, k)
			continue
		}
		files[k] = v
	}
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	return b.Bytes()
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// feedServer serves releases.json and the zip for version 8.3.99. hash overrides the checksum in the feed.
func feedServer(t *testing.T, zipBytes []byte, hash string) *httptest.Server {
	t.Helper()
	if hash == "" {
		hash = sum(zipBytes)
	}
	feed := fmt.Sprintf(`{"8.3":{"version":"8.3.99","nts-vs16-x64":{"zip":{"path":"php-8.3.99-nts-Win32-vs16-x64.zip","size":"1MB","sha256":%q}}}}`, hash)
	mux := http.NewServeMux()
	mux.HandleFunc("/releases.json", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(feed)) })
	mux.HandleFunc("/php-8.3.99-nts-Win32-vs16-x64.zip", func(w http.ResponseWriter, r *http.Request) { w.Write(zipBytes) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newInstaller(t *testing.T, srv *httptest.Server) *Installer {
	t.Helper()
	return &Installer{
		Home: filepath.Join(t.TempDir(), "php"), BaseURL: srv.URL, insecure: true, anyOS: true,
		Runtime: func() error { return nil }, Smoke: func(string) error { return nil },
	}
}

func leftovers(t *testing.T, home string) []string {
	t.Helper()
	entries, _ := os.ReadDir(home)
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestParseTheRealFeed(t *testing.T) {
	data, err := os.ReadFile("testdata/releases.json")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := ParseReleases(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(rel) < 5 {
		t.Fatalf("only %d releases", len(rel))
	}
	for i := 1; i < len(rel); i++ {
		if versionLess(rel[i-1].Version, rel[i].Version) {
			t.Errorf("not newest first: %s before %s", rel[i-1].Version, rel[i].Version)
		}
	}
	found := false
	for _, r := range rel {
		if !strings.Contains(r.Path, "-nts-") || !strings.HasSuffix(r.Path, "-x64.zip") || len(r.SHA256) != 64 || r.Size <= 0 {
			t.Errorf("bad release %+v", r)
		}
		if strings.HasPrefix(r.Version, "8.3.") {
			found = true
		}
	}
	if !found {
		t.Error("no 8.3 release")
	}
}

func TestParseRefusesWhatLooksWrong(t *testing.T) {
	for name, feed := range map[string]string{
		"not json":       `x`,
		"empty":          `{}`,
		"no nts":         `{"8.3":{"version":"8.3.1","ts-vs16-x64":{"zip":{"path":"php-8.3.1-Win32-vs16-x64.zip","size":"1MB","sha256":"` + strings.Repeat("a", 64) + `"}}}}`,
		"bad hash":       `{"8.3":{"version":"8.3.1","nts-vs16-x64":{"zip":{"path":"php-8.3.1-nts-Win32-vs16-x64.zip","size":"1MB","sha256":"zz"}}}}`,
		"path traversal": `{"8.3":{"version":"8.3.1","nts-vs16-x64":{"zip":{"path":"../php-8.3.1-nts-Win32-vs16-x64.zip","size":"1MB","sha256":"` + strings.Repeat("a", 64) + `"}}}}`,
		"other host":     `{"8.3":{"version":"8.3.1","nts-vs16-x64":{"zip":{"path":"http://evil.example/php-8.3.1-nts-Win32-vs16-x64.zip","size":"1MB","sha256":"` + strings.Repeat("a", 64) + `"}}}}`,
		"wrong version":  `{"8.3":{"version":"8.3.1","nts-vs16-x64":{"zip":{"path":"php-8.3.2-nts-Win32-vs16-x64.zip","size":"1MB","sha256":"` + strings.Repeat("a", 64) + `"}}}}`,
	} {
		if _, err := ParseReleases([]byte(feed)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestInstallListAndRemove(t *testing.T) {
	z := buildZip(t, nil)
	in := newInstaller(t, feedServer(t, z, ""))
	var steps []int
	err := in.Install(context.Background(), "8.3.99", func(p int, _ string) { steps = append(steps, p) })
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) < 3 || steps[len(steps)-1] != 100 {
		t.Errorf("progress: %v", steps)
	}
	if got := leftovers(t, in.Home); len(got) != 1 || got[0] != "8.3.99" {
		t.Errorf("home holds %v, want only 8.3.99 (no .part, no download)", got)
	}
	list, err := in.List()
	if err != nil || len(list) != 1 || list[0].Version != "8.3.99" || !strings.HasSuffix(filepath.ToSlash(list[0].Exe), "/8.3.99/php-cgi.exe") {
		t.Fatalf("list: %+v %v", list, err)
	}
	ini, _ := os.ReadFile(list[0].Ini)
	text := string(ini)
	wantDir := filepath.ToSlash(filepath.Join(in.Home, "8.3.99", "ext"))
	for _, want := range []string{`extension_dir = "` + wantDir + `"`, `date.timezone = "UTC"`, "\r\nextension=curl\r\n", "\r\nextension=mbstring\r\n", "\r\n;extension=gd\r\n", "memory_limit = 128M"} {
		if !strings.Contains(text, want) {
			t.Errorf("php.ini lacks %q:\n%s", want, text)
		}
	}
	// Prose lines of php.ini that mention a setting or an extension are left as they were (an example line made
	// "extension=mysqli" load twice in a real PHP).
	if !strings.Contains(text, "\r\n;   extension=curl\r\n") || !strings.Contains(text, "\r\n;   memory_limit = 1G\r\n") || strings.Count(text, "extension=curl") != 2 {
		t.Errorf("a documentation line was rewritten:\n%s", text)
	}
	if err := in.Install(context.Background(), "8.3.99", nil); !errors.Is(err, ErrInstalled) {
		t.Errorf("second install: %v", err)
	}

	st, err := in.IniState("8.3.99")
	if err != nil || st.Settings["memory_limit"] != "128M" || strings.Join(st.Available, ",") != "curl,mbstring" || strings.Join(st.Enabled, ",") != "curl,mbstring" {
		t.Errorf("state: %+v %v", st, err)
	}

	// A folder this package did not make is neither listed nor removable.
	if err := os.MkdirAll(filepath.Join(in.Home, "7.4.1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := in.Remove("7.4.1"); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("remove a foreign folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(in.Home, "7.4.1")); err != nil {
		t.Error("the foreign folder was deleted")
	}
	for _, bad := range []string{"..", "../x", "8.3", "8.3.99/..", ""} {
		if err := in.Remove(bad); !errors.Is(err, ErrNotInstalled) {
			t.Errorf("Remove(%q): %v", bad, err)
		}
	}
	if err := in.Remove("8.3.99"); err != nil {
		t.Fatal(err)
	}
	if list, _ := in.List(); len(list) != 0 {
		t.Errorf("still listed: %+v", list)
	}
}

func TestInstallRefusals(t *testing.T) {
	good := buildZip(t, nil)
	cases := map[string]struct {
		zip  []byte
		hash string
		want string
	}{
		"wrong checksum": {good, strings.Repeat("0", 64), "checksum"},
		"no php-cgi":     {buildZip(t, map[string]string{"php-cgi.exe": "<delete>"}), "", "no php-cgi.exe"},
		"dot dot":        {buildZip(t, map[string]string{"../evil.txt": "x"}), "", "unsafe name"},
		"nested dot dot": {buildZip(t, map[string]string{"a/../../evil.txt": "x"}), "", "unsafe name"},
		"absolute":       {buildZip(t, map[string]string{"/evil.txt": "x"}), "", "unsafe name"},
		"drive":          {buildZip(t, map[string]string{"C:evil.txt": "x"}), "", "unsafe name"},
		"backslash":      {buildZip(t, map[string]string{`..\evil.txt`: "x"}), "", "unsafe name"},
	}
	for name, c := range cases {
		in := newInstaller(t, feedServer(t, c.zip, c.hash))
		err := in.Install(context.Background(), "8.3.99", nil)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v (want %q)", name, err, c.want)
		}
		if got := leftovers(t, in.Home); len(got) != 0 {
			t.Errorf("%s: left behind %v", name, got)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(in.Home), "evil.txt")); err == nil {
			t.Errorf("%s: wrote outside the install folder", name)
		}
	}

	in := newInstaller(t, feedServer(t, good, ""))
	if err := in.Install(context.Background(), "8.3.100", nil); !errors.Is(err, ErrUnknownVersion) {
		t.Errorf("unknown version: %v", err)
	}
	for _, bad := range []string{"../8.3.99", "8.3.99/../x", "latest", ""} {
		if err := in.Install(context.Background(), bad, nil); !errors.Is(err, ErrUnknownVersion) {
			t.Errorf("Install(%q): %v", bad, err)
		}
	}
	in.Smoke = func(string) error { return errors.New("does not start") }
	if err := in.Install(context.Background(), "8.3.99", nil); err == nil || len(leftovers(t, in.Home)) != 0 {
		t.Errorf("a PHP that does not start was kept: %v %v", err, leftovers(t, in.Home))
	}
}

func TestTheRuntimeIsCheckedBeforeDownloading(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	in := newInstaller(t, srv)
	in.Runtime = func() error { return &RuntimeError{Missing: "vcruntime140.dll"} }
	err := in.Install(context.Background(), "8.3.99", nil)
	var re *RuntimeError
	if !errors.As(err, &re) || !strings.Contains(err.Error(), "aka.ms/vs/17/release/vc_redist.x64.exe") {
		t.Errorf("err = %v", err)
	}
	if hits.Load() != 0 {
		t.Error("it reached out to the network before checking the runtime")
	}
}

func TestOnlyOneInstallAtATime(t *testing.T) {
	z := buildZip(t, nil)
	in := newInstaller(t, feedServer(t, z, ""))
	started, release := make(chan struct{}), make(chan struct{})
	in.Smoke = func(string) error { close(started); <-release; return nil }
	done := make(chan error, 1)
	go func() { done <- in.Install(context.Background(), "8.3.99", nil) }()
	<-started
	if err := in.Install(context.Background(), "8.3.99", nil); !errors.Is(err, ErrBusy) {
		t.Errorf("second install: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestADownloadThatIsTooBigIsNotKept(t *testing.T) {
	old := maxZipSize
	maxZipSize = 100
	defer func() { maxZipSize = old }()
	in := newInstaller(t, feedServer(t, buildZip(t, nil), ""))
	if err := in.Install(context.Background(), "8.3.99", nil); err == nil || !strings.Contains(err.Error(), "larger than expected") {
		t.Errorf("err = %v", err)
	}
	if got := leftovers(t, in.Home); len(got) != 0 {
		t.Errorf("left behind %v", got)
	}
}

func TestRedirectsAndOtherHostsAreRefused(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("{}")) }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/releases.json", http.StatusFound)
	}))
	defer srv.Close()
	in := newInstaller(t, srv)
	if _, err := in.Releases(context.Background()); err == nil || !strings.Contains(err.Error(), "refusing a redirect") {
		t.Errorf("redirect off the host: %v", err)
	}
	if _, err := in.get(context.Background(), other.URL+"/x"); err == nil {
		t.Error("fetched a URL on another host")
	}
	// Plain http is refused for the real feed.
	real := &Installer{}
	if _, err := real.get(context.Background(), "http://windows.php.net/downloads/releases/releases.json"); err == nil {
		t.Error("fetched over plain http")
	}
}

func TestSettingsAndExtensionsAreEditedInPlace(t *testing.T) {
	in := newInstaller(t, feedServer(t, buildZip(t, nil), ""))
	if err := in.Install(context.Background(), "8.3.99", nil); err != nil {
		t.Fatal(err)
	}
	v, _ := in.Get("8.3.99")
	before, _ := os.ReadFile(v.Ini)

	err := in.ApplyIni("8.3.99",
		map[string]string{"memory_limit": "512M", "date.timezone": "Asia/Dhaka", "opcache.enable": "1", "max_input_vars": "5000"},
		map[string]bool{"curl": false, "mbstring": true})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(v.Ini)
	text := string(after)
	for _, want := range []string{"memory_limit = 512M\r\n", `date.timezone = "Asia/Dhaka"`, "\r\nopcache.enable = 1\r\n", "max_input_vars = 5000", "\r\n;extension=curl\r\n", "\r\nextension=mbstring\r\n", "upload_max_filesize = 2M", ";opcache.enable_cli=0"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Count(text, "\r\nmemory_limit") != 1 || strings.Contains(strings.ReplaceAll(text, "\r\n", ""), "\n") {
		t.Errorf("a line was duplicated or the line endings changed:\n%q", text)
	}
	if bak, _ := os.ReadFile(v.Ini + ".bak"); string(bak) != string(before) {
		t.Error("php.ini.bak is not the file as it was")
	}
	st, _ := in.IniState("8.3.99")
	if st.Settings["memory_limit"] != "512M" || st.Settings["date.timezone"] != "Asia/Dhaka" || strings.Join(st.Enabled, ",") != "mbstring" {
		t.Errorf("state: %+v", st)
	}
}

func TestBadSettingsWriteNothing(t *testing.T) {
	in := newInstaller(t, feedServer(t, buildZip(t, nil), ""))
	if err := in.Install(context.Background(), "8.3.99", nil); err != nil {
		t.Fatal(err)
	}
	v, _ := in.Get("8.3.99")
	before, _ := os.ReadFile(v.Ini)
	bad := []struct {
		s map[string]string
		e map[string]bool
	}{
		{s: map[string]string{"memory_limit": "256M\r\nauto_prepend_file=/evil.php"}},
		{s: map[string]string{"memory_limit": "lots"}},
		{s: map[string]string{"upload_max_filesize": "2M; x"}},
		{s: map[string]string{"date.timezone": `UTC"`}},
		{s: map[string]string{"display_errors": "maybe"}},
		{s: map[string]string{"opcache.enable": "2"}},
		{s: map[string]string{"auto_prepend_file": "/evil.php"}},
		{s: map[string]string{"extension_dir": "/evil"}},
		{e: map[string]bool{"gd": true}}, // no such dll in this build
		{e: map[string]bool{"../x": true}},
		{e: map[string]bool{"curl\n[x]": true}},
		{e: map[string]bool{"opcache": true}}, // handled by its own setting
	}
	for i, c := range bad {
		err := in.ApplyIni("8.3.99", c.s, c.e)
		var ve *ValueError
		if !errors.As(err, &ve) {
			t.Errorf("case %d: %v is not a ValueError", i, err)
		}
	}
	if after, _ := os.ReadFile(v.Ini); string(after) != string(before) {
		t.Error("php.ini changed although every request was refused")
	}
	if err := in.ApplyIni("9.9.9", nil, nil); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("unknown version: %v", err)
	}
}

func TestInstallIsRefusedOutsideWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this is the rule for other systems")
	}
	in := &Installer{Home: t.TempDir()}
	if err := in.Install(context.Background(), "8.3.99", nil); !errors.Is(err, ErrUnsupported) {
		t.Errorf("err = %v", err)
	}
}

func TestOnlyThePHPProjectsHostsAreAllowed(t *testing.T) {
	in := &Installer{}
	for host, want := range map[string]bool{
		"windows.php.net": true, "downloads.php.net": true,
		"php.net": false, "evil.example": false, "windows.php.net.evil.example": false, "downloads.php.net:8443": false,
	} {
		if got := in.hostAllowed(host); got != want {
			t.Errorf("hostAllowed(%q) = %v, want %v", host, got, want)
		}
	}
	// A test server's own host is allowed for its own feed, and nothing else is.
	custom := &Installer{BaseURL: "https://mirror.example/php/"}
	if !custom.hostAllowed("mirror.example") || custom.hostAllowed("downloads.php.net") {
		t.Error("a custom base allows only its own host")
	}
}
