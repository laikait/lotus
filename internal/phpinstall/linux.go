package phpinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// LinuxManager is the Linux counterpart of Installer: PHP comes from the system's packages (apt or dnf), not from a
// zip. It finds the php-cgi programs that are installed (a "found" version can be used with one click, no root needed),
// and, when the LOTUS service runs as root, installs a package version. Each version it uses has a folder in Home
// with lotus-php.json (which names the php-cgi program) and a php.ini of its own, copied from the system's, which is
// the file the panel edits. Extensions are packages on Linux, so they are shown but not switched here.
type LinuxManager struct {
	Home string // such as /etc/lotus/php

	// Run runs a program and returns its combined output. Nil: exec, with env added to the environment.
	Run func(ctx context.Context, env []string, name string, args ...string) ([]byte, error)
	// Root says whether packages can be installed. Nil: the process runs as root.
	Root func() bool
	// Which finds a program in PATH. Nil: exec.LookPath.
	Which func(name string) (string, error)
	// SearchDirs are the places php-cgi programs are looked for; nil: the usual ones. Tests set them.
	SearchGlobs []string

	anyOS bool // tests

	busy sync.Mutex // one install at a time
}

var defaultSearchGlobs = []string{
	"/usr/bin/php-cgi", "/usr/bin/php-cgi[0-9]*", "/usr/local/bin/php-cgi", "/usr/local/bin/php-cgi[0-9]*",
	"/usr/lib/cgi-bin/php[0-9]*", "/opt/remi/php[0-9]*/root/usr/bin/php-cgi",
}

var (
	pkgNameRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]{0,80}$`)
	phpVersionRe = regexp.MustCompile(`PHP ([0-9]{1,2}\.[0-9]{1,2}\.[0-9]{1,3})`)
	anyVersionRe = regexp.MustCompile(`([0-9]{1,2}\.[0-9]{1,2}\.[0-9]{1,3})`)
)

// linuxExtensions are the extension packages installed with a PHP version, when the repository has them.
var linuxExtensions = []string{"curl", "mbstring", "mysql", "xml", "zip", "gd", "intl", "sqlite3", "bcmath", "opcache"}

func (m *LinuxManager) run(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	if m.Run != nil {
		return m.Run(ctx, env, name, args...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	return cmd.CombinedOutput()
}

func (m *LinuxManager) isRoot() bool {
	if m.Root != nil {
		return m.Root()
	}
	return os.Geteuid() == 0
}

func (m *LinuxManager) which(name string) (string, error) {
	if m.Which != nil {
		return m.Which(name)
	}
	return exec.LookPath(name)
}

// Supported says whether this is a system the manager works on.
func (m *LinuxManager) Supported() bool { return runtime.GOOS == "linux" || m.anyOS }

// CheckRuntime has nothing to check on Linux.
func (m *LinuxManager) CheckRuntime() error { return nil }

// CanInstall says whether the service can install packages (it runs as root).
func (m *LinuxManager) CanInstall() bool { return m.isRoot() }

// Kind names the way versions come: "packages".
func (m *LinuxManager) Kind() string { return "packages" }

// ExtensionsEditable is false: extensions are packages.
func (m *LinuxManager) ExtensionsEditable() bool { return false }

func (m *LinuxManager) manager() string {
	if _, err := m.which("apt-get"); err == nil {
		return "apt"
	}
	if _, err := m.which("dnf"); err == nil {
		return "dnf"
	}
	return ""
}

// found is a php-cgi program that is installed on the system.
type found struct {
	Exe     string
	Version string
}

// discover looks in the usual places for php-cgi programs and asks each one for its version. One entry per version.
func (m *LinuxManager) discover(ctx context.Context) []found {
	globs := m.SearchGlobs
	if globs == nil {
		globs = defaultSearchGlobs
	}
	seen := map[string]bool{}
	var out []found
	for _, g := range globs {
		files, _ := filepath.Glob(g)
		sort.Strings(files)
		for _, f := range files {
			fi, err := os.Stat(f)
			if err != nil || fi.IsDir() || (runtime.GOOS != "windows" && fi.Mode()&0o111 == 0) || strings.HasSuffix(f, ".ini") {
				continue
			}
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			outb, err := m.run(cctx, nil, f, "-v")
			cancel()
			if err != nil {
				continue
			}
			mt := phpVersionRe.FindSubmatch(outb)
			if mt == nil || seen[string(mt[1])] {
				continue
			}
			seen[string(mt[1])] = true
			out = append(out, found{Exe: f, Version: string(mt[1])})
		}
	}
	return out
}

// List returns the versions in use: the folders of Home.
func (m *LinuxManager) List() ([]Installed, error) {
	entries, err := os.ReadDir(m.Home)
	if errors.Is(err, os.ErrNotExist) {
		return []Installed{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Installed{}
	for _, e := range entries {
		if !e.IsDir() || !ValidVersion(e.Name()) {
			continue
		}
		dir := filepath.Join(m.Home, e.Name())
		data, err := os.ReadFile(filepath.Join(dir, metaFile))
		if err != nil {
			continue
		}
		var mt Meta
		if json.Unmarshal(data, &mt) != nil || mt.Version != e.Name() || !filepath.IsAbs(mt.Exe) {
			continue
		}
		if fi, err := os.Stat(mt.Exe); err != nil || fi.IsDir() {
			continue
		}
		out = append(out, Installed{Version: mt.Version, Dir: dir, Exe: mt.Exe, Ini: filepath.Join(dir, "php.ini"), InstalledAt: mt.InstalledAt})
	}
	sort.Slice(out, func(i, j int) bool { return versionLess(out[j].Version, out[i].Version) })
	return out, nil
}

// Get returns one version in use.
func (m *LinuxManager) Get(version string) (Installed, error) {
	list, err := m.List()
	if err != nil {
		return Installed{}, err
	}
	for _, v := range list {
		if v.Version == version {
			return v, nil
		}
	}
	return Installed{}, ErrNotInstalled
}

// Remove stops using a version: its folder (the copy of php.ini) is deleted. The system's packages stay.
func (m *LinuxManager) Remove(version string) error {
	v, err := m.Get(version)
	if err != nil {
		return err
	}
	return os.RemoveAll(v.Dir)
}

// Releases lists what can be used or installed: the php-cgi programs found on the system that are not in use yet
// (Found is their path), then the package versions the repository offers.
func (m *LinuxManager) Releases(ctx context.Context) ([]Release, error) {
	var out []Release
	have := map[string]bool{}
	for _, f := range m.discover(ctx) {
		parts := strings.Split(f.Version, ".")
		out = append(out, Release{Branch: parts[0] + "." + parts[1], Version: f.Version, Found: f.Exe})
		have[f.Version] = true
	}
	for _, c := range m.candidates(ctx) {
		if !have[c.Version] {
			out = append(out, c)
		}
	}
	if len(out) == 0 && m.manager() == "" {
		return nil, errors.New("no php-cgi was found and there is no apt or dnf to install one with")
	}
	sort.Slice(out, func(i, j int) bool { return versionLess(out[j].Version, out[i].Version) })
	return out, nil
}

// candidates asks the package manager which PHP versions it can install. Path holds the package prefix.
func (m *LinuxManager) candidates(ctx context.Context) []Release {
	switch m.manager() {
	case "apt":
		return m.aptCandidates(ctx)
	case "dnf":
		return m.dnfCandidates(ctx)
	}
	return nil
}

var aptCgiRe = regexp.MustCompile(`^(php[0-9]+\.[0-9]+)-cgi\b`)

func (m *LinuxManager) aptCandidates(ctx context.Context) []Release {
	outb, err := m.run(ctx, nil, "apt-cache", "search", "--names-only", `^php[0-9]+\.[0-9]+-cgi$`)
	if err != nil {
		return nil
	}
	var out []Release
	for _, line := range strings.Split(string(outb), "\n") {
		mt := aptCgiRe.FindStringSubmatch(strings.TrimSpace(line))
		if mt == nil {
			continue
		}
		pol, err := m.run(ctx, nil, "apt-cache", "policy", mt[1]+"-cgi")
		if err != nil {
			continue
		}
		cand := ""
		for _, l := range strings.Split(string(pol), "\n") {
			if t := strings.TrimSpace(l); strings.HasPrefix(t, "Candidate:") {
				cand = strings.TrimSpace(strings.TrimPrefix(t, "Candidate:"))
			}
		}
		v := anyVersionRe.FindString(strings.TrimPrefix(cand, "1:")) // Debian epochs: 1:8.2.7+dfsg
		if cand == "" || cand == "(none)" || v == "" || !ValidVersion(v) {
			continue
		}
		parts := strings.Split(v, ".")
		out = append(out, Release{Branch: parts[0] + "." + parts[1], Version: v, Path: mt[1]})
	}
	return out
}

var dnfCgiRe = regexp.MustCompile(`^((?:php[0-9]+-)?php)-cgi\.[A-Za-z0-9_]+\s+(\S+)`)

func (m *LinuxManager) dnfCandidates(ctx context.Context) []Release {
	outb, err := m.run(ctx, nil, "dnf", "-q", "list", "--available", "php-cgi", "php*-php-cgi")
	if err != nil {
		return nil
	}
	var out []Release
	for _, line := range strings.Split(string(outb), "\n") {
		mt := dnfCgiRe.FindStringSubmatch(strings.TrimSpace(line))
		if mt == nil {
			continue
		}
		v := anyVersionRe.FindString(strings.TrimPrefix(mt[2], "0:"))
		if v == "" || !ValidVersion(v) {
			continue
		}
		parts := strings.Split(v, ".")
		out = append(out, Release{Branch: parts[0] + "." + parts[1], Version: v, Path: mt[1]})
	}
	return out
}

// Install makes a version usable. A version that is already on the system ("found") is only registered; no root is
// needed. Otherwise the package manager installs it, which needs the service to run as root.
func (m *LinuxManager) Install(ctx context.Context, version string, progress Progress) error {
	if !m.Supported() {
		return ErrUnsupported
	}
	if !m.busy.TryLock() {
		return ErrBusy
	}
	defer m.busy.Unlock()

	progress.report(2, "Looking for PHP "+version)
	rel, err := m.Releases(ctx)
	if err != nil {
		return err
	}
	var want *Release
	for i := range rel {
		if rel[i].Version == version {
			want = &rel[i]
		}
	}
	if want == nil {
		return ErrUnknownVersion
	}
	if _, err := os.Stat(filepath.Join(m.Home, version)); err == nil {
		return ErrInstalled
	}
	exe := want.Found
	if exe == "" {
		if exe, err = m.installPackages(ctx, want, progress); err != nil {
			return err
		}
	}
	progress.report(92, "Making a php.ini of its own")
	if err := m.adopt(ctx, version, exe); err != nil {
		return err
	}
	progress.report(100, "PHP "+version+" is ready")
	return nil
}

// installPackages runs apt-get or dnf for one version and returns the php-cgi program it installed.
func (m *LinuxManager) installPackages(ctx context.Context, want *Release, progress Progress) (string, error) {
	pm := m.manager()
	prefix := want.Path
	if !m.isRoot() {
		return "", fmt.Errorf("the LOTUS service does not run as root, so it can't install packages. Run this as root, then press Use: %s", m.commandLine(pm, prefix, nil))
	}
	if !pkgNameRe.MatchString(prefix) {
		return "", errors.New("the package name looks wrong")
	}
	pkgs := []string{}
	var argv []string
	switch pm {
	case "apt":
		// A stale package index is the usual reason an install fails with "404 Not Found": refresh it first. A
		// failure here is not fatal (the machine may be offline except for a local mirror); the install says if it matters.
		progress.report(5, "Refreshing the package list")
		uctx, ucancel := context.WithTimeout(ctx, 5*time.Minute)
		_, _ = m.run(uctx, []string{"DEBIAN_FRONTEND=noninteractive", "LC_ALL=C"}, "apt-get", "update", "-qq")
		ucancel()
		pkgs = append(pkgs, prefix+"-cgi")
		for _, e := range linuxExtensions {
			if m.packageExists(ctx, "apt", prefix+"-"+e) {
				pkgs = append(pkgs, prefix+"-"+e)
			}
		}
		argv = append([]string{"apt-get", "install", "-y", "--no-install-recommends"}, pkgs...)
	case "dnf":
		base := want.Path // php  or php83-php
		pkgs = append(pkgs, base+"-cgi")
		for _, e := range linuxExtensions {
			name := map[string]string{"mysql": "mysqlnd", "curl": "common", "sqlite3": "pdo"}[e]
			if name == "" {
				name = e
			}
			if m.packageExists(ctx, "dnf", base+"-"+name) && !contains(pkgs, base+"-"+name) {
				pkgs = append(pkgs, base+"-"+name)
			}
		}
		argv = append([]string{"dnf", "install", "-y"}, pkgs...)
	default:
		return "", errors.New("there is no apt or dnf on this system")
	}
	for _, p := range pkgs {
		if !pkgNameRe.MatchString(p) {
			return "", errors.New("the package name looks wrong")
		}
	}
	progress.report(10, "Installing "+strings.Join(pkgs, " "))
	ictx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	outb, err := m.run(ictx, []string{"DEBIAN_FRONTEND=noninteractive", "LC_ALL=C"}, argv[0], argv[1:]...)
	if err != nil {
		return "", fmt.Errorf("the package manager failed: %v: %s", err, lastLines(string(outb), 4))
	}
	progress.report(80, "Packages installed")
	for _, f := range m.discover(ctx) {
		if f.Version == want.Version {
			return f.Exe, nil
		}
	}
	return "", errors.New("the packages were installed, but no php-cgi of this version was found afterwards")
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// commandLine is what a person would run to install the packages themselves.
func (m *LinuxManager) commandLine(pm, prefix string, extra []string) string {
	switch pm {
	case "apt":
		return "apt-get install -y " + prefix + "-cgi " + prefix + "-curl " + prefix + "-mbstring " + prefix + "-mysql " + prefix + "-xml " + prefix + "-zip"
	case "dnf":
		return "dnf install -y " + prefix + "-cgi " + prefix + "-mbstring " + prefix + "-mysqlnd " + prefix + "-xml"
	}
	return "install the php-cgi package of your distribution"
}

// packageExists asks the package manager whether a package of that name is in the repositories.
func (m *LinuxManager) packageExists(ctx context.Context, pm, name string) bool {
	if !pkgNameRe.MatchString(name) {
		return false
	}
	switch pm {
	case "apt":
		out, err := m.run(ctx, nil, "apt-cache", "policy", name)
		return err == nil && strings.Contains(string(out), "Candidate:") && !strings.Contains(string(out), "Candidate: (none)")
	case "dnf":
		out, err := m.run(ctx, nil, "dnf", "-q", "list", "--available", name)
		return err == nil && strings.Contains(string(out), name)
	}
	return false
}

// adopt registers a php-cgi program as an installed version: its folder, lotus-php.json and a php.ini copied from
// the one the system uses.
func (m *LinuxManager) adopt(ctx context.Context, version, exe string) error {
	if !filepath.IsAbs(exe) {
		return errors.New("the php-cgi program has no full path")
	}
	dir := filepath.Join(m.Home, version)
	part := dir + ".part"
	_ = os.RemoveAll(part)
	if err := os.MkdirAll(part, 0o755); err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(part)
		}
	}()
	ini := []byte("[PHP]\n")
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	info, _ := m.run(cctx, nil, exe, "-i")
	cancel()
	if src := iniPathFrom(string(info)); src != "" {
		if data, err := os.ReadFile(src); err == nil && len(data) < 1<<20 {
			ini = data
		}
	}
	if err := os.WriteFile(filepath.Join(part, "php.ini"), ini, 0o644); err != nil {
		return err
	}
	meta, _ := json.MarshalIndent(Meta{Version: version, Source: "system package", Exe: exe, InstalledAt: time.Now().UTC()}, "", "  ")
	if err := os.WriteFile(filepath.Join(part, metaFile), meta, 0o644); err != nil {
		return err
	}
	if err := os.Rename(part, dir); err != nil {
		return err
	}
	ok = true
	return nil
}

var iniLineRe = regexp.MustCompile(`(?m)^Loaded Configuration File\s*=>\s*(.+?)\s*$`)

// iniPathFrom reads the php.ini path from `php-cgi -i`.
func iniPathFrom(info string) string {
	if mt := iniLineRe.FindStringSubmatch(info); mt != nil && filepath.IsAbs(mt[1]) {
		return mt[1]
	}
	return ""
}

// IniState reads the settings of the version's own php.ini. The extensions are the modules PHP has loaded.
func (m *LinuxManager) IniState(version string) (IniState, error) {
	v, err := m.Get(version)
	if err != nil {
		return IniState{}, err
	}
	data, err := os.ReadFile(v.Ini)
	if err != nil {
		return IniState{}, err
	}
	t := parseIni(data)
	st := IniState{Settings: map[string]string{}, Enabled: []string{}, Available: []string{}}
	for _, s := range Settings {
		if val, ok := t.get(s.Key); ok {
			st.Settings[s.Key] = val
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if outb, err := m.run(ctx, nil, v.Exe, "-c", v.Ini, "-m"); err == nil {
		for _, l := range strings.Split(string(outb), "\n") {
			if l = strings.ToLower(strings.TrimSpace(l)); l != "" && !strings.HasPrefix(l, "[") && extNameRe.MatchString(l) {
				st.Enabled = append(st.Enabled, l)
			}
		}
		sort.Strings(st.Enabled)
		st.Available = st.Enabled
	}
	return st, nil
}

// ApplyIni changes settings in the version's php.ini. Extensions can't be switched here.
func (m *LinuxManager) ApplyIni(version string, settings map[string]string, extensions map[string]bool) error {
	v, err := m.Get(version)
	if err != nil {
		return err
	}
	if len(extensions) > 0 {
		return &ValueError{Msg: "extensions are packages on Linux: install or remove them with the package manager (for example apt install php8.3-gd)"}
	}
	return applySettings(v.Ini, settings)
}
