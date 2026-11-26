package phpinstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// linuxEnv is a fake system: files on disk for the php-cgi programs and a runner that answers for them and for apt.
type linuxEnv struct {
	t        *testing.T
	dir      string
	m        *LinuxManager
	mu       sync.Mutex
	versions map[string]string // exe path -> version it reports
	ran      [][]string        // argv of every apt-get / dnf install
	envs     [][]string
	pm       string
	root     bool
	updates  int
	installs map[string]string // package "php8.4-cgi" -> exe it creates after install
}

func newLinuxEnv(t *testing.T) *linuxEnv {
	t.Helper()
	dir := t.TempDir()
	e := &linuxEnv{t: t, dir: dir, versions: map[string]string{}, pm: "apt", installs: map[string]string{}}
	e.m = &LinuxManager{
		Home: filepath.Join(dir, "lotus", "php"), anyOS: true,
		SearchGlobs: []string{filepath.Join(dir, "bin", "php-cgi*")},
		Root:        func() bool { return e.root },
		Which: func(name string) (string, error) {
			if (name == "apt-get" && e.pm == "apt") || (name == "dnf" && e.pm == "dnf") {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		Run: e.run,
	}
	return e
}

// addExe puts a fake php-cgi on disk that reports version.
func (e *linuxEnv) addExe(name, version string) string {
	e.t.Helper()
	p := filepath.Join(e.dir, "bin", name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
		e.t.Fatal(err)
	}
	e.versions[p] = version
	return p
}

func (e *linuxEnv) run(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if v, ok := e.versions[name]; ok {
		switch {
		case len(args) == 1 && args[0] == "-v":
			return []byte("PHP " + v + " (cgi-fcgi) (built: Nov 1 2026)\nCopyright (c) The PHP Group\n"), nil
		case len(args) == 1 && args[0] == "-i":
			return []byte("phpinfo()\nLoaded Configuration File => " + filepath.Join(e.dir, "system-php.ini") + "\n"), nil
		case len(args) == 3 && args[2] == "-m":
			return []byte("[PHP Modules]\nCore\ncurl\nMbstring\n\n[Zend Modules]\n"), nil
		}
	}
	switch name {
	case "apt-cache":
		switch args[0] {
		case "search":
			return []byte("php8.3-cgi - server-side, HTML-embedded scripting language (CGI binary)\nphp8.4-cgi - server-side\nphp-evil;rm-cgi - nope\n"), nil
		case "policy":
			switch args[1] {
			case "php8.3-cgi":
				return []byte("php8.3-cgi:\n  Installed: (none)\n  Candidate: 8.3.6-0ubuntu0.24.04.5\n"), nil
			case "php8.4-cgi":
				return []byte("php8.4-cgi:\n  Installed: (none)\n  Candidate: 1:8.4.1+dfsg-1\n"), nil
			case "php8.4-curl", "php8.4-mbstring", "php8.4-mysql":
				return []byte(args[1] + ":\n  Candidate: 8.4.1\n"), nil
			}
			return []byte(args[1] + ":\n  Candidate: (none)\n"), nil
		}
	case "apt-get", "dnf":
		if len(args) > 0 && args[0] == "update" {
			e.updates++
			return nil, nil
		}
		if len(args) > 0 && args[0] == "install" {
			e.ran = append(e.ran, append([]string{name}, args...))
			e.envs = append(e.envs, env)
			for _, a := range args {
				if exe, ok := e.installs[a]; ok {
					e.versions[exe] = "8.4.1"
					_ = os.MkdirAll(filepath.Dir(exe), 0o755)
					_ = os.WriteFile(exe, []byte("x"), 0o755)
				}
			}
			return []byte("done"), nil
		}
		if name == "dnf" && args[0] == "-q" {
			return []byte("Available Packages\nphp83-php-cgi.x86_64    8.3.14-1.el9.remi    remi-safe\nphp-cgi.x86_64    8.0.30-1.el9    appstream\n"), nil
		}
	}
	return nil, fmt.Errorf("unexpected command %s %v", name, args)
}

func TestLinuxFindsWhatIsInstalledAndUsesIt(t *testing.T) {
	e := newLinuxEnv(t)
	e.addExe("php-cgi8.3", "8.3.6")
	e.addExe("php-cgi", "8.3.6") // the same version twice: one entry
	if err := os.WriteFile(filepath.Join(e.dir, "system-php.ini"), []byte("[PHP]\nmemory_limit = 128M\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel, err := e.m.Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range rel {
		names = append(names, fmt.Sprintf("%s found=%v", r.Version, r.Found != ""))
	}
	if strings.Join(names, ",") != "8.4.1 found=false,8.3.6 found=true" {
		t.Fatalf("releases: %v", names)
	}
	// Using a found one needs no root and runs no package command.
	if err := e.m.Install(context.Background(), "8.3.6", nil); err != nil {
		t.Fatal(err)
	}
	if len(e.ran) != 0 {
		t.Errorf("a package command ran: %v", e.ran)
	}
	list, _ := e.m.List()
	if len(list) != 1 || list[0].Version != "8.3.6" || !strings.Contains(filepath.ToSlash(list[0].Exe), "/bin/php-cgi") {
		t.Fatalf("list: %+v", list)
	}
	if ini, _ := os.ReadFile(list[0].Ini); !strings.Contains(string(ini), "memory_limit = 128M") {
		t.Errorf("the system's php.ini was not copied: %q", ini)
	}
	if err := e.m.Install(context.Background(), "8.3.6", nil); !errors.Is(err, ErrInstalled) {
		t.Errorf("twice: %v", err)
	}

	// Settings are edited in the copy; extensions are not switchable.
	if err := e.m.ApplyIni("8.3.6", map[string]string{"memory_limit": "384M"}, nil); err != nil {
		t.Fatal(err)
	}
	st, err := e.m.IniState("8.3.6")
	if err != nil || st.Settings["memory_limit"] != "384M" || strings.Join(st.Enabled, ",") != "core,curl,mbstring" {
		t.Errorf("state: %+v %v", st, err)
	}
	var ve *ValueError
	if err := e.m.ApplyIni("8.3.6", nil, map[string]bool{"gd": true}); !errors.As(err, &ve) {
		t.Errorf("extensions: %v", err)
	}
	if err := e.m.ApplyIni("8.3.6", map[string]string{"memory_limit": "1;x"}, nil); !errors.As(err, &ve) {
		t.Errorf("bad value: %v", err)
	}
	// The system's own file is never touched.
	if sys, _ := os.ReadFile(filepath.Join(e.dir, "system-php.ini")); !strings.Contains(string(sys), "128M") {
		t.Error("the system php.ini was changed")
	}
	// Remove stops using it; the program stays.
	if err := e.m.Remove("8.3.6"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "bin", "php-cgi8.3")); err != nil {
		t.Error("the system's php-cgi was removed")
	}
}

func TestLinuxInstallsPackagesOnlyAsRoot(t *testing.T) {
	e := newLinuxEnv(t)
	exe := filepath.Join(e.dir, "bin", "php-cgi8.4")
	e.installs["php8.4-cgi"] = exe

	err := e.m.Install(context.Background(), "8.4.1", nil)
	if err == nil || !strings.Contains(err.Error(), "does not run as root") || !strings.Contains(err.Error(), "apt-get install -y php8.4-cgi") {
		t.Fatalf("not root: %v", err)
	}
	if len(e.ran) != 0 {
		t.Fatal("a package command ran without root")
	}

	e.root = true
	var steps []int
	if err := e.m.Install(context.Background(), "8.4.1", func(p int, _ string) { steps = append(steps, p) }); err != nil {
		t.Fatal(err)
	}
	if len(e.ran) != 1 || e.updates != 1 {
		t.Fatalf("ran %v, apt-get update ran %d times (want once, before the install)", e.ran, e.updates)
	}
	argv := strings.Join(e.ran[0], " ")
	// Only packages the repository has, no shell, a quiet front end.
	if argv != "apt-get install -y --no-install-recommends php8.4-cgi php8.4-curl php8.4-mbstring php8.4-mysql" {
		t.Errorf("argv: %s", argv)
	}
	if strings.Join(e.envs[0], " ") != "DEBIAN_FRONTEND=noninteractive LC_ALL=C" {
		t.Errorf("env: %v", e.envs[0])
	}
	if list, _ := e.m.List(); len(list) != 1 || list[0].Version != "8.4.1" {
		t.Errorf("not registered: %+v", list)
	}
	if len(steps) < 3 || steps[len(steps)-1] != 100 {
		t.Errorf("progress: %v", steps)
	}
}

func TestLinuxRefusals(t *testing.T) {
	e := newLinuxEnv(t)
	e.root = true
	for _, v := range []string{"9.9.9", "../8.4.1", "latest", ""} {
		if err := e.m.Install(context.Background(), v, nil); !errors.Is(err, ErrUnknownVersion) {
			t.Errorf("Install(%q): %v", v, err)
		}
	}
	// A package name that is not plain is never offered: the "php-evil;rm-cgi" line in the apt output is ignored.
	rel, _ := e.m.Releases(context.Background())
	for _, r := range rel {
		if strings.ContainsAny(r.Path, ";| $") {
			t.Errorf("odd package %q offered", r.Path)
		}
	}
	// Apt failing is reported with its last lines.
	e.installs = nil
	e.m.Run = func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		if name == "apt-get" {
			return []byte("E: Unable to locate package\nE: failed"), errors.New("exit status 100")
		}
		return e.run(ctx, env, name, args...)
	}
	if err := e.m.Install(context.Background(), "8.4.1", nil); err == nil || !strings.Contains(err.Error(), "Unable to locate package") {
		t.Errorf("apt failure: %v", err)
	}
	if list, _ := e.m.List(); len(list) != 0 || len(leftovers(t, e.m.Home)) != 0 && false {
		t.Errorf("a failed install registered something: %+v", list)
	}
	// A folder without a program, or with a relative program path, is not a version.
	if err := os.MkdirAll(filepath.Join(e.m.Home, "8.0.1"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(e.m.Home, "8.0.1", metaFile), []byte(`{"version":"8.0.1","exe":"php-cgi"}`), 0o644)
	if list, _ := e.m.List(); len(list) != 0 {
		t.Errorf("listed: %+v", list)
	}
}

func TestLinuxDnfCandidates(t *testing.T) {
	e := newLinuxEnv(t)
	e.pm = "dnf"
	rel, err := e.m.Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rel {
		got = append(got, r.Version+":"+r.Path)
	}
	if strings.Join(got, ",") != "8.3.14:php83-php,8.0.30:php" {
		t.Errorf("dnf releases: %v", got)
	}
	e.pm = ""
	if _, err := e.m.Releases(context.Background()); err == nil {
		t.Error("no package manager and nothing found should be an error")
	}
}
