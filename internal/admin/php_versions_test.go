package admin

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/phpinstall"
)

type fakeInstaller struct {
	mu        sync.Mutex
	home      string
	supported bool
	runtime   error
	installed map[string]bool
	gate      chan struct{} // when set, Install waits on it
	failWith  error
	ini       map[string]string
	ext       map[string]bool
	removed   []string
}

func newFake(home string) *fakeInstaller {
	return &fakeInstaller{home: home, supported: true, installed: map[string]bool{}, ini: map[string]string{"memory_limit": "128M"}, ext: map[string]bool{"curl": true, "mbstring": false}}
}

func (f *fakeInstaller) Supported() bool     { return f.supported }
func (f *fakeInstaller) CheckRuntime() error { return f.runtime }
func (f *fakeInstaller) Releases(context.Context) ([]phpinstall.Release, error) {
	return []phpinstall.Release{{Branch: "8.4", Version: "8.4.28", Size: 40 << 20}, {Branch: "8.3", Version: "8.3.99", Size: 35 << 20}}, nil
}
func (f *fakeInstaller) inst(v string) phpinstall.Installed {
	dir := filepath.Join(f.home, v)
	return phpinstall.Installed{Version: v, Dir: dir, Exe: filepath.Join(dir, "php-cgi.exe"), Ini: filepath.Join(dir, "php.ini")}
}
func (f *fakeInstaller) List() ([]phpinstall.Installed, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []phpinstall.Installed{}
	for v := range f.installed {
		out = append(out, f.inst(v))
	}
	return out, nil
}
func (f *fakeInstaller) Get(v string) (phpinstall.Installed, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.installed[v] {
		return phpinstall.Installed{}, phpinstall.ErrNotInstalled
	}
	return f.inst(v), nil
}
func (f *fakeInstaller) Install(ctx context.Context, v string, p phpinstall.Progress) error {
	p(10, "Downloading")
	if f.gate != nil {
		<-f.gate
	}
	if f.failWith != nil {
		return f.failWith
	}
	f.mu.Lock()
	f.installed[v] = true
	f.mu.Unlock()
	return nil
}
func (f *fakeInstaller) Remove(v string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.installed, v)
	f.removed = append(f.removed, v)
	return nil
}
func (f *fakeInstaller) IniState(v string) (phpinstall.IniState, error) {
	return phpinstall.IniState{Settings: f.ini, Enabled: []string{"curl"}, Available: []string{"curl", "mbstring"}}, nil
}
func (f *fakeInstaller) ApplyIni(v string, s map[string]string, e map[string]bool) error {
	for k, val := range s {
		if strings.Contains(val, "\n") {
			return &phpinstall.ValueError{Msg: "bad value"}
		}
		f.ini[k] = val
	}
	for k, on := range e {
		f.ext[k] = on
	}
	return nil
}

func TestPHPVersionsInstallPoolAndRemove(t *testing.T) {
	e, dir, cookie, csrf := siteEnv(t)
	fake := newFake(filepath.Join(dir, "php"))
	e.srv.opts.PHPInstall = fake
	restarted := []string{}
	e.srv.opts.PHPRestart = func(n string) bool { restarted = append(restarted, n); return true }

	rec := e.do("GET", "/v1/php-versions", "", "", cookie)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"supported":true`) || !strings.Contains(rec.Body.String(), `"version":"8.3.99"`) || !strings.Contains(rec.Body.String(), `"installed":[]`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}

	// Install: 202 with a job; the job is polled until it is done.
	fake.gate = make(chan struct{})
	rec = e.do("POST", "/v1/php-versions", `{"version":"8.3.99"}`, csrf, cookie)
	if rec.Code != 202 {
		t.Fatalf("install: %d %s", rec.Code, rec.Body)
	}
	id := between(rec.Body.String(), `"job":"`, `"`)
	if rec = e.do("POST", "/v1/php-versions", `{"version":"8.4.28"}`, csrf, cookie); rec.Code != 409 {
		t.Errorf("a second install while one runs: %d %s", rec.Code, rec.Body)
	}
	if rec = e.do("GET", "/v1/php-jobs/"+id, "", "", cookie); !strings.Contains(rec.Body.String(), `"state":"running"`) {
		t.Errorf("job: %s", rec.Body)
	}
	close(fake.gate)
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec = e.do("GET", "/v1/php-jobs/"+id, "", "", cookie)
		if strings.Contains(rec.Body.String(), `"state":"done"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the job never finished: %s", rec.Body)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if rec = e.do("GET", "/v1/php-jobs/nope", "", "", cookie); rec.Code != 404 {
		t.Errorf("unknown job: %d", rec.Code)
	}

	// Make a pool for it; the site form then labels it by version.
	rec = e.do("POST", "/v1/php-versions/8.3.99/pool", `{}`, csrf, cookie)
	if rec.Code != 200 {
		t.Fatalf("pool: %d %s", rec.Code, rec.Body)
	}
	rec = e.do("GET", "/v1/php-versions", "", "", cookie)
	if !strings.Contains(rec.Body.String(), `"pools":["php83"]`) {
		t.Errorf("the version should list its pool: %s", rec.Body)
	}
	if rec = e.do("GET", "/v1/site-files", "", "", cookie); !strings.Contains(rec.Body.String(), `"php_labels":{"php83":"PHP 8.3.99 (php83)"}`) {
		t.Errorf("labels: %s", rec.Body)
	}
	if rec = e.do("POST", "/v1/php-versions/8.3.99/pool", `{"name":"php83"}`, csrf, cookie); rec.Code != 409 {
		t.Errorf("duplicate pool name: %d", rec.Code)
	}
	if rec = e.do("POST", "/v1/php-versions/8.3.99/pool", `{}`, csrf, cookie); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Errorf("second pool gets a new name: %d %s", rec.Code, rec.Body)
	}

	// Settings: read, change, and the pools that use the version are restarted.
	if rec = e.do("GET", "/v1/php-versions/8.3.99/ini", "", "", cookie); !strings.Contains(rec.Body.String(), `"memory_limit":"128M"`) || !strings.Contains(rec.Body.String(), `"setting_info"`) {
		t.Errorf("ini: %s", rec.Body)
	}
	rec = e.do("PUT", "/v1/php-versions/8.3.99/ini", `{"settings":{"memory_limit":"512M"},"extensions":{"mbstring":true}}`, csrf, cookie)
	if rec.Code != 200 || fake.ini["memory_limit"] != "512M" || !fake.ext["mbstring"] || len(restarted) == 0 {
		t.Errorf("ini save: %d %s restarted=%v", rec.Code, rec.Body, restarted)
	}
	if rec = e.do("PUT", "/v1/php-versions/8.3.99/ini", `{"settings":{"memory_limit":"1\n2"}}`, csrf, cookie); rec.Code != 422 {
		t.Errorf("bad value: %d", rec.Code)
	}
	if rec = e.do("PUT", "/v1/php-versions/9.9.9/ini", `{}`, csrf, cookie); rec.Code != 404 {
		t.Errorf("unknown version: %d", rec.Code)
	}

	// Remove: refused while a pool uses it, allowed after the pools are deleted.
	if rec = e.do("DELETE", "/v1/php-versions/8.3.99", "", csrf, cookie); rec.Code != 409 || !strings.Contains(rec.Body.String(), "php83") {
		t.Errorf("remove in use: %d %s", rec.Code, rec.Body)
	}
	for _, p := range []string{"php83", "php83-99"} {
		if rec = e.do("DELETE", "/v1/php-pools/"+p, "", csrf, cookie); rec.Code != 200 {
			t.Fatalf("delete pool %s: %d %s", p, rec.Code, rec.Body)
		}
	}
	if rec = e.do("DELETE", "/v1/php-versions/8.3.99", "", csrf, cookie); rec.Code != 200 || len(fake.removed) != 1 {
		t.Errorf("remove: %d %s", rec.Code, rec.Body)
	}
	if rec = e.do("DELETE", "/v1/php-versions/8.3.99", "", csrf, cookie); rec.Code != 404 {
		t.Errorf("remove twice: %d", rec.Code)
	}
}

func TestPHPInstallRefusals(t *testing.T) {
	e, dir, cookie, csrf := siteEnv(t)
	fake := newFake(filepath.Join(dir, "php"))
	e.srv.opts.PHPInstall = fake

	for _, bad := range []string{`{"version":"../8.3.99"}`, `{"version":"latest"}`, `{"version":""}`, `{"version":"8.3.99","extra":1}`} {
		if rec := e.do("POST", "/v1/php-versions", bad, csrf, cookie); rec.Code != 422 && rec.Code != 400 {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	// A missing runtime is said before anything starts, and the page can show the message.
	fake.runtime = &phpinstall.RuntimeError{Missing: "vcruntime140.dll"}
	if rec := e.do("POST", "/v1/php-versions", `{"version":"8.3.99"}`, csrf, cookie); rec.Code != 422 || !strings.Contains(rec.Body.String(), "Visual C++") {
		t.Errorf("runtime missing: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/v1/php-versions", "", "", cookie); !strings.Contains(rec.Body.String(), `"runtime_ok":false`) || !strings.Contains(rec.Body.String(), "vc_redist.x64.exe") {
		t.Errorf("list with a missing runtime: %s", rec.Body)
	}
	fake.runtime = nil
	// A failed install shows its reason.
	fake.failWith = errors.New("the downloaded file does not match the checksum")
	rec := e.do("POST", "/v1/php-versions", `{"version":"8.3.99"}`, csrf, cookie)
	id := between(rec.Body.String(), `"job":"`, `"`)
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec = e.do("GET", "/v1/php-jobs/"+id, "", "", cookie)
		if strings.Contains(rec.Body.String(), `"state":"failed"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never failed: %s", rec.Body)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(rec.Body.String(), "checksum") {
		t.Errorf("reason lost: %s", rec.Body)
	}
	// Not Windows: nothing to install.
	fake.supported = false
	if rec := e.do("GET", "/v1/php-versions", "", "", cookie); !strings.Contains(rec.Body.String(), `"supported":false`) {
		t.Errorf("unsupported: %s", rec.Body)
	}
	if rec := e.do("POST", "/v1/php-versions", `{"version":"8.3.99"}`, csrf, cookie); rec.Code != 422 {
		t.Errorf("install when unsupported: %d", rec.Code)
	}
	// Viewers can look, not change.
	vcookie, vcsrf := e.signIn("vic", "another long secret")
	if rec := e.do("GET", "/v1/php-versions", "", "", vcookie); rec.Code != 200 {
		t.Errorf("viewer list: %d", rec.Code)
	}
	for _, c := range [][3]string{{"POST", "/v1/php-versions", `{"version":"8.3.99"}`}, {"DELETE", "/v1/php-versions/8.3.99", ""}, {"PUT", "/v1/php-versions/8.3.99/ini", `{}`}, {"POST", "/v1/php-versions/8.3.99/pool", `{}`}} {
		if rec := e.do(c[0], c[1], c[2], vcsrf, vcookie); rec.Code != 403 {
			t.Errorf("viewer %s %s: %d", c[0], c[1], rec.Code)
		}
	}
}

func TestDefaultPoolNames(t *testing.T) {
	if got := defaultPoolName("8.3.37", map[string]bool{}); got != "php83" {
		t.Errorf("got %q", got)
	}
	if got := defaultPoolName("8.3.37", map[string]bool{"php83": true}); got != "php83-37" {
		t.Errorf("got %q", got)
	}
}
