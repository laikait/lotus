package phpinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSmoke says "with the ionCube PHP Loader" when the php.ini loads one, as the real php-cgi -v does.
func fakeSmoke(exe, ini string) (string, error) {
	data, _ := os.ReadFile(ini)
	out := "PHP 8.3.99 (cgi-fcgi)\n"
	if ionLineRe.MatchString(strings.ReplaceAll(string(data), "\r\n", "\n")) || strings.Contains(string(data), "ioncube_loader") {
		out += "    with the ionCube PHP Loader v14.0\n"
	}
	return out, nil
}

func ionServer(t *testing.T, pack string, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, pack) {
			w.Write(body)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func installedWindows(t *testing.T) *Installer {
	t.Helper()
	in := newInstaller(t, feedServer(t, buildZip(t, nil), ""))
	if err := in.Install(context.Background(), "8.3.99", nil); err != nil {
		t.Fatal(err)
	}
	in.IonSmoke = fakeSmoke
	return in
}

func TestIonCubeOnWindowsInstallEnableDisable(t *testing.T) {
	in := installedWindows(t)
	zipBytes := buildZip(t, map[string]string{"ioncube/ioncube_loader_win_8.3.dll": "loader", "php-cgi.exe": "<delete>", "php.ini-production": "<delete>", "ext/php_curl.dll": "<delete>", "ext/php_mbstring.dll": "<delete>", "ext/php_opcache.dll": "<delete>"})
	srv := ionServer(t, "ioncube_loaders_win_nonts_vc16_x86-64.zip", zipBytes)
	in.IonCubeBase = srv.URL

	st, err := in.IonCube("8.3.99")
	if err != nil || !st.Available || st.Downloaded || st.Enabled {
		t.Fatalf("before: %+v %v", st, err)
	}
	var steps []int
	if err := in.InstallIonCube(context.Background(), "8.3.99", func(p int, _ string) { steps = append(steps, p) }); err != nil {
		t.Fatal(err)
	}
	if len(steps) < 2 || steps[len(steps)-1] != 100 {
		t.Errorf("progress: %v", steps)
	}
	v, _ := in.Get("8.3.99")
	if data, err := os.ReadFile(filepath.Join(v.Dir, "ioncube_loader_win_8.3.dll")); err != nil || string(data) != "loader" {
		t.Errorf("the loader file: %q %v", data, err)
	}
	ini, _ := os.ReadFile(v.Ini)
	lines := strings.Split(strings.ReplaceAll(string(ini), "\r\n", "\n"), "\n")
	if lines[0] != ionComment || !strings.HasPrefix(lines[1], `zend_extension = "`) || !strings.HasSuffix(lines[1], `ioncube_loader_win_8.3.dll"`) {
		t.Errorf("the loader must be first in php.ini:\n%s", strings.Join(lines[:4], "\n"))
	}
	if st, _ := in.IonCube("8.3.99"); !st.Enabled || !st.Downloaded {
		t.Errorf("after: %+v", st)
	}
	// Other settings survive; turning it on twice does not repeat the line.
	if err := in.SetIonCube("8.3.99", true); err != nil {
		t.Fatal(err)
	}
	ini, _ = os.ReadFile(v.Ini)
	if strings.Count(string(ini), "ioncube_loader_win") != 1 || !strings.Contains(string(ini), "memory_limit = 128M") {
		t.Errorf("php.ini after a second enable:\n%s", ini)
	}
	if err := in.SetIonCube("8.3.99", false); err != nil {
		t.Fatal(err)
	}
	ini, _ = os.ReadFile(v.Ini)
	if strings.Contains(string(ini), "ioncube") || strings.Contains(string(ini), "ionCube") {
		t.Errorf("not removed:\n%s", ini)
	}
	if st, _ := in.IonCube("8.3.99"); st.Enabled || !st.Downloaded {
		t.Errorf("after disable: %+v", st)
	}
}

func TestIonCubeThatDoesNotWorkIsRolledBack(t *testing.T) {
	in := installedWindows(t)
	zipBytes := buildZip(t, map[string]string{"ioncube/ioncube_loader_win_8.3.dll": "loader"})
	in.IonCubeBase = ionServer(t, "ioncube_loaders_win_nonts_vc16_x86-64.zip", zipBytes).URL
	v, _ := in.Get("8.3.99")
	before, _ := os.ReadFile(v.Ini)
	in.IonSmoke = func(exe, ini string) (string, error) {
		return "PHP Warning: Failed loading ioncube_loader", errors.New("exit status 1")
	}
	err := in.InstallIonCube(context.Background(), "8.3.99", nil)
	if err == nil || !strings.Contains(err.Error(), "php.ini is unchanged") {
		t.Fatalf("err = %v", err)
	}
	if after, _ := os.ReadFile(v.Ini); string(after) != string(before) {
		t.Error("php.ini was left changed")
	}
	if _, err := os.Stat(filepath.Join(v.Dir, "ioncube_loader_win_8.3.dll")); err == nil {
		t.Error("the loader file was kept")
	}
	// PHP that starts but does not report the loader is a failure too.
	in.IonSmoke = func(exe, ini string) (string, error) { return "PHP 8.3.99", nil }
	if err := in.InstallIonCube(context.Background(), "8.3.99", nil); err == nil || !strings.Contains(err.Error(), "did not load") {
		t.Errorf("silent loader: %v", err)
	}
	if after, _ := os.ReadFile(v.Ini); string(after) != string(before) {
		t.Error("php.ini was left changed after the silent loader")
	}
}

func TestIonCubeRefusals(t *testing.T) {
	in := installedWindows(t)
	// A PHP version ionCube has no loader for.
	if got, _, ok, why := ionPack("windows", "amd64", "8.0", false); ok || got != "" || !strings.Contains(why, "8.0") {
		t.Errorf("8.0: %v %q", ok, why)
	}
	if _, _, ok, _ := ionPack("windows", "amd64", "8.6", false); ok {
		t.Error("8.6 should not be offered")
	}
	for minor, pack := range map[string]string{"7.4": "ioncube_loaders_win_vc15_x86-64.zip", "8.2": "ioncube_loaders_win_nonts_vc16_x86-64.zip", "8.4": "ioncube_loaders_win_nonts_vc17_x86-64.zip"} {
		if got, _, ok, _ := ionPack("windows", "amd64", minor, false); !ok || got != pack {
			t.Errorf("%s: %q %v", minor, got, ok)
		}
	}
	// A redirect to another host is refused.
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("x")) }))
	defer other.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL+"/x", http.StatusFound) }))
	defer redir.Close()
	in.IonCubeBase = redir.URL
	if err := in.InstallIonCube(context.Background(), "8.3.99", nil); err == nil || !strings.Contains(err.Error(), "refusing a redirect") {
		t.Errorf("redirect: %v", err)
	}
	// The pack without the file for this version says so.
	in.IonCubeBase = ionServer(t, "ioncube_loaders_win_nonts_vc16_x86-64.zip", buildZip(t, nil)).URL
	if err := in.InstallIonCube(context.Background(), "8.3.99", nil); err == nil || !strings.Contains(err.Error(), "no ioncube_loader_win_8.3.dll") {
		t.Errorf("missing entry: %v", err)
	}
	// A server that has nothing.
	in.IonCubeBase = ionServer(t, "other.zip", nil).URL
	if err := in.InstallIonCube(context.Background(), "8.3.99", nil); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("404: %v", err)
	}
	// Turning it on before it is downloaded.
	if err := in.SetIonCube("8.3.99", true); err == nil || !strings.Contains(err.Error(), "not downloaded") {
		t.Errorf("enable without a file: %v", err)
	}
	if _, err := in.IonCube("9.9.9"); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("unknown version: %v", err)
	}
}

func targz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return b.Bytes()
}

func TestIonCubeOnLinuxPicksThreadSafeFilesForZTS(t *testing.T) {
	e := newLinuxEnv(t)
	e.addExe("php-cgi8.3", "8.3.6")
	if err := os.WriteFile(filepath.Join(e.dir, "system-php.ini"), []byte("[PHP]\nmemory_limit = 128M\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Install(context.Background(), "8.3.6", nil); err != nil {
		t.Fatal(err)
	}
	pack := targz(t, map[string]string{"ioncube/ioncube_loader_lin_8.3.so": "nts-loader", "ioncube/ioncube_loader_lin_8.3_ts.so": "ts-loader", "ioncube/README.txt": "x"})
	e.m.IonCubeBase = ionServer(t, "ioncube_loaders_lin_x86-64.tar.gz", pack).URL
	e.m.IonSmoke = fakeSmoke

	if err := e.m.InstallIonCube(context.Background(), "8.3.6", nil); err != nil {
		t.Fatal(err)
	}
	v, _ := e.m.Get("8.3.6")
	if data, err := os.ReadFile(filepath.Join(v.Dir, "ioncube_loader_lin_8.3.so")); err != nil || string(data) != "nts-loader" {
		t.Errorf("loader: %q %v", data, err)
	}
	if st, _ := e.m.IonCube("8.3.6"); !st.Enabled || !st.Downloaded || !st.Available {
		t.Errorf("state: %+v", st)
	}
	if err := e.m.SetIonCube("8.3.6", false); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.m.IonCube("8.3.6"); st.Enabled {
		t.Errorf("still enabled: %+v", st)
	}
	// A pack that is not a gzip file is refused with a clear message.
	e.m.IonCubeBase = ionServer(t, "ioncube_loaders_lin_x86-64.tar.gz", []byte("not gzip")).URL
	if err := e.m.InstallIonCube(context.Background(), "8.3.6", nil); err == nil || !strings.Contains(err.Error(), "not a gzip") {
		t.Errorf("bad pack: %v", err)
	}
	// ZTS PHP gets the _ts file.
	if _, entry, ok, _ := ionPack("linux", "amd64", "8.3", true); !ok || entry != "ioncube/ioncube_loader_lin_8.3_ts.so" {
		t.Errorf("zts entry: %q", entry)
	}
}
