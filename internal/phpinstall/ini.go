package phpinstall

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// SettingInfo describes one php.ini setting the panel may change.
type SettingInfo struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Hint    string `json:"hint"`
	pattern *regexp.Regexp
	quote   bool // the value is written in double quotes
}

// Settings are the only php.ini settings the panel reads and writes. Each has its own value pattern, because the
// value is written into a file that PHP reads.
var Settings = []SettingInfo{
	{Key: "memory_limit", Label: "Memory limit", Hint: "such as 256M, or -1 for no limit", pattern: regexp.MustCompile(`^(-1|[0-9]{1,6}[KMG]?)$`)},
	{Key: "upload_max_filesize", Label: "Largest upload", Hint: "such as 64M", pattern: regexp.MustCompile(`^[0-9]{1,6}[KMG]?$`)},
	{Key: "post_max_size", Label: "Largest form post", Hint: "such as 64M; keep it at least as big as the largest upload", pattern: regexp.MustCompile(`^[0-9]{1,6}[KMG]?$`)},
	{Key: "max_execution_time", Label: "Longest run time (seconds)", Hint: "0 for no limit", pattern: regexp.MustCompile(`^[0-9]{1,5}$`)},
	{Key: "max_input_vars", Label: "Most form fields", Hint: "such as 1000", pattern: regexp.MustCompile(`^[0-9]{1,6}$`)},
	{Key: "date.timezone", Label: "Time zone", Hint: "such as UTC or Asia/Dhaka", pattern: regexp.MustCompile(`^[A-Za-z0-9_+/-]{1,64}$`), quote: true},
	{Key: "display_errors", Label: "Show errors in the page", Hint: "On or Off; keep it Off on a live site", pattern: regexp.MustCompile(`^(On|Off)$`)},
	{Key: "opcache.enable", Label: "Opcode cache (opcache)", Hint: "1 (on) or 0 (off)", pattern: regexp.MustCompile(`^[01]$`)},
}

// defaultExtensions are enabled in a new php.ini when the build has them.
var defaultExtensions = []string{"curl", "fileinfo", "gd", "intl", "mbstring", "exif", "mysqli", "openssl", "pdo_mysql", "pdo_sqlite", "sqlite3", "zip"}

var extNameRe = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)

// ValueError means a setting or an extension name is not acceptable.
type ValueError struct{ Msg string }

func (e *ValueError) Error() string { return e.Msg }

// IniState is what the settings page shows for one version.
type IniState struct {
	Settings   map[string]string `json:"settings"`
	Enabled    []string          `json:"enabled"`
	Available  []string          `json:"available"`
	SettingNfo []SettingInfo     `json:"-"`
}

// iniText is a php.ini held as lines, edited one line at a time so that everything else stays as it was.
type iniText struct {
	lines []string
	eol   string
}

func parseIni(data []byte) *iniText {
	text := string(data)
	eol := "\n"
	if strings.Contains(text, "\r\n") {
		eol = "\r\n"
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return &iniText{lines: strings.Split(strings.TrimRight(text, "\n"), "\n"), eol: eol}
}

func (t *iniText) bytes() []byte { return []byte(strings.Join(t.lines, t.eol) + t.eol) }

func keyRe(key string, commented bool) *regexp.Regexp {
	if commented {
		return regexp.MustCompile(`^\s*;` + regexp.QuoteMeta(key) + `\s*=`) // ";key =": not a prose line such as ";   key = ..."
	}
	return regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=`)
}

// get returns the value of the last uncommented line for key.
func (t *iniText) get(key string) (string, bool) {
	re := keyRe(key, false)
	val, found := "", false
	for _, l := range t.lines {
		if re.MatchString(l) {
			val, found = iniValue(l[strings.Index(l, "=")+1:]), true
		}
	}
	return val, found
}

// iniValue reads the value part of a line: a quoted string, or text up to a ; comment.
func iniValue(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, `"`) {
		if end := strings.Index(s[1:], `"`); end >= 0 {
			return s[1 : 1+end]
		}
		return strings.Trim(s, `"`)
	}
	if i := strings.Index(s, ";"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// set makes `key = value` the setting: it rewrites the last uncommented line, else the first commented one, else
// adds a line at the end.
func (t *iniText) set(key, value string, quote bool) {
	line := key + " = " + value
	if quote {
		line = key + ` = "` + value + `"`
	}
	re := keyRe(key, false)
	last := -1
	for i, l := range t.lines {
		if re.MatchString(l) {
			last = i
		}
	}
	if last < 0 {
		cre := keyRe(key, true)
		for i, l := range t.lines {
			if cre.MatchString(l) {
				last = i
				break
			}
		}
	}
	if last >= 0 {
		t.lines[last] = line
		return
	}
	t.lines = append(t.lines, line)
}

// extension names that are loaded, from uncommented extension= lines (php_curl.dll and curl both count).
func (t *iniText) enabledExtensions() map[string]bool {
	out := map[string]bool{}
	re := regexp.MustCompile(`^\s*extension\s*=\s*"?([A-Za-z0-9_./\\-]+)"?\s*(;.*)?$`)
	for _, l := range t.lines {
		if m := re.FindStringSubmatch(l); m != nil {
			n := strings.ToLower(filepath.Base(strings.ReplaceAll(m[1], "\\", "/")))
			n = strings.TrimSuffix(strings.TrimPrefix(n, "php_"), ".dll")
			out[n] = true
		}
	}
	return out
}

// setExtension enables or disables one extension line.
func (t *iniText) setExtension(name string, on bool) {
	re := regexp.MustCompile(`^\s*;?extension\s*=\s*"?(php_)?` + regexp.QuoteMeta(name) + `(\.dll)?"?\s*(;.*)?$`)
	found := false
	for i, l := range t.lines {
		if re.MatchString(l) {
			found = true
			if on {
				t.lines[i] = "extension=" + name
			} else {
				t.lines[i] = ";extension=" + name
			}
		}
	}
	if !found && on {
		t.lines = append(t.lines, "extension="+name)
	}
}

// availableExtensions lists the extension DLLs that ship in a version's ext folder.
func availableExtensions(dir string) []string {
	files, _ := filepath.Glob(filepath.Join(dir, "ext", "php_*.dll"))
	out := []string{}
	for _, f := range files {
		n := strings.TrimSuffix(strings.TrimPrefix(strings.ToLower(filepath.Base(f)), "php_"), ".dll")
		if n == "opcache" || !extNameRe.MatchString(n) { // opcache is a zend extension, set with its own setting
			continue
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ini-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// writeDefaultIni makes php.ini in a freshly unpacked version from php.ini-production.
// final is the folder it will have once renamed: extension_dir must name that, not the temporary folder.
func writeDefaultIni(dir, final string) error {
	data, err := os.ReadFile(filepath.Join(dir, "php.ini-production"))
	if err != nil {
		data = []byte("[PHP]\n")
	}
	t := parseIni(data)
	t.set("extension_dir", filepath.ToSlash(filepath.Join(final, "ext")), true)
	t.set("date.timezone", "UTC", true)
	have := map[string]bool{}
	for _, n := range availableExtensions(dir) {
		have[n] = true
	}
	for _, n := range defaultExtensions {
		if have[n] {
			t.setExtension(n, true)
		}
	}
	return writeFileAtomic(filepath.Join(dir, "php.ini"), t.bytes())
}

// IniState reads the settings, the enabled extensions and the extensions that exist for an installed version.
func (in *Installer) IniState(version string) (IniState, error) {
	v, err := in.Get(version)
	if err != nil {
		return IniState{}, err
	}
	data, err := os.ReadFile(v.Ini)
	if err != nil {
		return IniState{}, err
	}
	t := parseIni(data)
	st := IniState{Settings: map[string]string{}, Enabled: []string{}, Available: availableExtensions(v.Dir)}
	for _, s := range Settings {
		if val, ok := t.get(s.Key); ok {
			st.Settings[s.Key] = val
		}
	}
	on := t.enabledExtensions()
	for _, n := range st.Available {
		if on[n] {
			st.Enabled = append(st.Enabled, n)
		}
	}
	return st, nil
}

// ApplyIni changes settings and extensions of an installed version's php.ini. Nothing is written unless every value
// is acceptable. The previous file is kept as php.ini.bak.
func (in *Installer) ApplyIni(version string, settings map[string]string, extensions map[string]bool) error {
	v, err := in.Get(version)
	if err != nil {
		return err
	}
	byKey := map[string]SettingInfo{}
	for _, s := range Settings {
		byKey[s.Key] = s
	}
	for k, val := range settings {
		s, ok := byKey[k]
		if !ok {
			return &ValueError{Msg: fmt.Sprintf("%q is not a setting the panel changes", k)}
		}
		if !s.pattern.MatchString(val) {
			return &ValueError{Msg: fmt.Sprintf("%s: %q is not acceptable (%s)", s.Label, val, s.Hint)}
		}
	}
	have := map[string]bool{}
	for _, n := range availableExtensions(v.Dir) {
		have[n] = true
	}
	for n := range extensions {
		if !extNameRe.MatchString(n) || !have[n] {
			return &ValueError{Msg: fmt.Sprintf("this PHP has no extension %q", n)}
		}
	}
	old, err := os.ReadFile(v.Ini)
	if err != nil {
		return err
	}
	t := parseIni(old)
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.set(k, settings[k], byKey[k].quote)
	}
	names := make([]string, 0, len(extensions))
	for n := range extensions {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		t.setExtension(n, extensions[n])
	}
	if err := os.WriteFile(v.Ini+".bak", old, 0o644); err != nil {
		return err
	}
	return writeFileAtomic(v.Ini, t.bytes())
}
