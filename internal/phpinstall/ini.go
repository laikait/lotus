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
	Group   string `json:"group"`
	Type    string `json:"type"` // text, switch (On or Off)
	pattern *regexp.Regexp
	quote   bool // the value is written in double quotes
}

var (
	sizeRe   = regexp.MustCompile(`^[0-9]{1,6}[KMG]?$`)
	numberRe = regexp.MustCompile(`^[0-9]{1,7}$`)
	switchRe = regexp.MustCompile(`^(On|Off)$`)
	// pathRe is a file or folder path: no quote, no semicolon, no line break, and no backslash at the end (it would
	// escape the closing quote). An empty value clears the setting.
	pathRe = regexp.MustCompile(`^([A-Za-z0-9_ ./:\\-]{0,259}[A-Za-z0-9_ ./:-])?$`)
)

// Settings are the only php.ini settings the panel reads and writes. Each has its own value pattern, because the
// value is written into a file that PHP reads. Settings in the same Group are shown together.
var Settings = []SettingInfo{
	{Group: "Limits", Key: "memory_limit", Label: "Memory limit", Hint: "such as 256M, or -1 for no limit", pattern: regexp.MustCompile(`^(-1|[0-9]{1,6}[KMG]?)$`)},
	{Group: "Limits", Key: "max_execution_time", Label: "Longest run time (seconds)", Hint: "0 for no limit", pattern: regexp.MustCompile(`^[0-9]{1,5}$`)},
	{Group: "Limits", Key: "max_input_time", Label: "Longest input time (seconds)", Hint: "time to read a request; -1 uses the run time", pattern: regexp.MustCompile(`^(-1|[0-9]{1,5})$`)},
	{Group: "Limits", Key: "max_input_vars", Label: "Most form fields", Hint: "such as 1000", pattern: numberRe},
	{Group: "Limits", Key: "realpath_cache_size", Label: "Path cache size", Hint: "such as 4096K; WordPress and Laravel run faster with 4M", pattern: sizeRe},
	{Group: "Limits", Key: "realpath_cache_ttl", Label: "Path cache lifetime (seconds)", Hint: "such as 120", pattern: numberRe},

	{Group: "Uploads", Key: "file_uploads", Label: "Allow file uploads", Hint: "On or Off", Type: "switch", pattern: switchRe},
	{Group: "Uploads", Key: "upload_max_filesize", Label: "Largest upload", Hint: "such as 64M", pattern: sizeRe},
	{Group: "Uploads", Key: "post_max_size", Label: "Largest form post", Hint: "such as 64M; keep it at least as big as the largest upload", pattern: sizeRe},
	{Group: "Uploads", Key: "max_file_uploads", Label: "Most files in one upload", Hint: "such as 20", pattern: numberRe},
	{Group: "Uploads", Key: "upload_tmp_dir", Label: "Upload temporary folder", Hint: "a folder PHP can write to; empty for the system folder", pattern: pathRe, quote: true},

	{Group: "Errors and logs", Key: "display_errors", Label: "Show errors in the page", Hint: "keep it Off on a live site", Type: "switch", pattern: switchRe},
	{Group: "Errors and logs", Key: "display_startup_errors", Label: "Show start-up errors", Hint: "keep it Off on a live site", Type: "switch", pattern: switchRe},
	{Group: "Errors and logs", Key: "log_errors", Label: "Write errors to a log", Hint: "On or Off", Type: "switch", pattern: switchRe},
	{Group: "Errors and logs", Key: "error_log", Label: "Error log file", Hint: "a file path; empty for the server's log", pattern: pathRe, quote: true},
	{Group: "Errors and logs", Key: "error_reporting", Label: "Which errors to report", Hint: "such as E_ALL & ~E_DEPRECATED & ~E_STRICT", pattern: regexp.MustCompile(`^[A-Z_0-9 &|~^()!-]{1,150}$`)},

	{Group: "Behaviour", Key: "date.timezone", Label: "Time zone", Hint: "such as UTC or Asia/Dhaka", pattern: regexp.MustCompile(`^[A-Za-z0-9_+/-]{1,64}$`), quote: true},
	{Group: "Behaviour", Key: "default_charset", Label: "Default character set", Hint: "such as UTF-8", pattern: regexp.MustCompile(`^[A-Za-z0-9_.-]{1,40}$`), quote: true},
	{Group: "Behaviour", Key: "short_open_tag", Label: "Short open tag (<?)", Hint: "keep it Off unless old code needs it", Type: "switch", pattern: switchRe},
	{Group: "Behaviour", Key: "expose_php", Label: "Tell visitors PHP is used", Hint: "Off hides the PHP version", Type: "switch", pattern: switchRe},
	{Group: "Behaviour", Key: "allow_url_fopen", Label: "Open web addresses as files", Hint: "On or Off; some plugins need On", Type: "switch", pattern: switchRe},
	{Group: "Behaviour", Key: "allow_url_include", Label: "Include code from web addresses", Hint: "keep it Off", Type: "switch", pattern: switchRe},
	{Group: "Behaviour", Key: "disable_functions", Label: "Functions switched off", Hint: "names separated by commas; empty for none", pattern: regexp.MustCompile(`^[A-Za-z0-9_,]{0,1000}$`)},
	{Group: "Behaviour", Key: "curl.cainfo", Label: "CA certificate file for curl", Hint: "a cacert.pem path, so PHP can reach HTTPS sites", pattern: pathRe, quote: true},
	{Group: "Behaviour", Key: "openssl.cafile", Label: "CA certificate file for OpenSSL", Hint: "a cacert.pem path", pattern: pathRe, quote: true},

	{Group: "Sessions", Key: "session.save_path", Label: "Session folder", Hint: "a folder PHP can write to; empty for the system folder", pattern: pathRe, quote: true},
	{Group: "Sessions", Key: "session.gc_maxlifetime", Label: "Session lifetime (seconds)", Hint: "such as 1440", pattern: numberRe},
	{Group: "Sessions", Key: "session.cookie_httponly", Label: "Session cookie hidden from scripts", Hint: "On is safer", Type: "switch", pattern: switchRe},
	{Group: "Sessions", Key: "session.cookie_secure", Label: "Session cookie only over HTTPS", Hint: "On only if the whole site uses HTTPS", Type: "switch", pattern: switchRe},
	{Group: "Sessions", Key: "session.use_strict_mode", Label: "Refuse unknown session ids", Hint: "On is safer", Type: "switch", pattern: switchRe},

	{Group: "Opcode cache", Key: "opcache.enable", Label: "Opcode cache (opcache)", Hint: "On or Off", Type: "switch", pattern: switchRe},
	{Group: "Opcode cache", Key: "opcache.memory_consumption", Label: "Cache memory (MB)", Hint: "such as 128", pattern: numberRe},
	{Group: "Opcode cache", Key: "opcache.interned_strings_buffer", Label: "Strings memory (MB)", Hint: "such as 8", pattern: numberRe},
	{Group: "Opcode cache", Key: "opcache.max_accelerated_files", Label: "Most cached files", Hint: "such as 10000", pattern: numberRe},
	{Group: "Opcode cache", Key: "opcache.validate_timestamps", Label: "Check files for changes", Hint: "Off is faster, but code changes need a PHP restart", Type: "switch", pattern: switchRe},
	{Group: "Opcode cache", Key: "opcache.revalidate_freq", Label: "Check files every (seconds)", Hint: "such as 2", pattern: numberRe},
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

// checkSettings validates settings against the fixed list.
func checkSettings(settings map[string]string) (map[string]SettingInfo, error) {
	byKey := map[string]SettingInfo{}
	for _, s := range Settings {
		byKey[s.Key] = s
	}
	for k, val := range settings {
		s, ok := byKey[k]
		if !ok {
			return nil, &ValueError{Msg: fmt.Sprintf("%q is not a setting the panel changes", k)}
		}
		if !s.pattern.MatchString(val) {
			return nil, &ValueError{Msg: fmt.Sprintf("%s: %q is not acceptable (%s)", s.Label, val, s.Hint)}
		}
	}
	return byKey, nil
}

// applySettings changes settings in a php.ini and keeps the old file as .bak. Nothing is written if a value is refused.
func applySettings(iniPath string, settings map[string]string) error {
	return editIni(iniPath, settings, nil)
}

func editIni(iniPath string, settings map[string]string, extensions map[string]bool) error {
	byKey, err := checkSettings(settings)
	if err != nil {
		return err
	}
	old, err := os.ReadFile(iniPath)
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
	if err := os.WriteFile(iniPath+".bak", old, 0o644); err != nil {
		return err
	}
	return writeFileAtomic(iniPath, t.bytes())
}

// ApplyIni changes settings and extensions of an installed version's php.ini. Nothing is written unless every value
// is acceptable. The previous file is kept as php.ini.bak.
func (in *Installer) ApplyIni(version string, settings map[string]string, extensions map[string]bool) error {
	v, err := in.Get(version)
	if err != nil {
		return err
	}
	if _, err := checkSettings(settings); err != nil {
		return err
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
	return editIni(v.Ini, settings, extensions)
}

// CanInstall says whether installs can run (always, for the zip installer).
func (in *Installer) CanInstall() bool { return true }

// Kind names the way versions come: "zip" (official builds).
func (in *Installer) Kind() string { return "zip" }

// ExtensionsEditable is true: extensions are DLLs switched in php.ini.
func (in *Installer) ExtensionsEditable() bool { return true }
