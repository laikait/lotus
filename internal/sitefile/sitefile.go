// Package sitefile makes, changes and removes the site files in the sites.d
// folder next to the main configuration file. The lotus site command and the
// web admin both use it, so there is one way to write a site.
//
// A site file made here has a marker line first and a fixed layout. Only such
// a file can be changed again through this package: a file somebody edited by
// hand (anything beyond the layout) is shown, but never rewritten.
//
// Every change is checked against the whole configuration. If the result is not
// valid the old file is put back and the problems are returned.
package sitefile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/laikait/lotus/internal/config"
	"github.com/laikait/lotus/internal/config/diag"
	"github.com/laikait/lotus/internal/config/loader"
)

// Marker is the first line of a site file made by this package.
const Marker = "# Managed by LOTUS (lotus site, the web admin). Keep this line and the layout, or the admin shows the site read-only."

// IncludeLine is what the main configuration needs, inside its server block, to read the folder.
const IncludeLine = `include "sites.d/*.conf"`

// Spec is what a site made here can say.
type Spec struct {
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
	Root    string   `json:"root"`
	Index   []string `json:"index"`
	TLS     string   `json:"tls"` // "off" or "automatic"

	// Spec version 2: the template and the settings of the Advanced section. All are optional.
	Template        string            `json:"template,omitempty"`         // static (default), php, wordpress, laravel, proxy
	PHPBackend      string            `json:"php_backend,omitempty"`      // name of a php_backend, for the PHP templates
	ProxyURL        string            `json:"proxy_url,omitempty"`        // http(s)://host:port, for the proxy template
	RateRequests    int               `json:"rate_requests,omitempty"`    // 0: no rate limit
	RatePer         string            `json:"rate_per,omitempty"`         // 1s, 1m or 1h
	RateBurst       int               `json:"rate_burst,omitempty"`       // 0: the server's default
	Compression     string            `json:"compression,omitempty"`      // "" (server default), on, off
	SecurityHeaders string            `json:"security_headers,omitempty"` // "", basic
	Symlinks        string            `json:"symlinks,omitempty"`         // "", follow, owner_match, deny
	Dotfiles        string            `json:"dotfiles,omitempty"`         // "", deny, allow
	HTAccess        bool              `json:"htaccess,omitempty"`
	ErrorPages      map[string]string `json:"error_pages,omitempty"` // "404" -> "/404.html"
	AccessLog       string            `json:"access_log,omitempty"`
	ErrorLog        string            `json:"error_log,omitempty"`
	Listeners       []string          `json:"listeners,omitempty"` // names of the listeners the site answers on; empty: all
}

// Entry is a site file found in the folder.
type Entry struct {
	Spec
	File     string `json:"file"`
	Editable bool   `json:"editable"` // made here and not changed by hand
}

// InvalidError means the configuration would not be valid with the change, so nothing was changed.
type InvalidError struct{ Diagnostics []diag.Diagnostic }

func (e *InvalidError) Error() string {
	for _, d := range e.Diagnostics {
		if d.Severity == diag.Error {
			return "the configuration would not be valid: " + d.Message
		}
	}
	return "the configuration would not be valid"
}

// ValidationError means a value of the spec is not acceptable; Msg says which and why.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// Errors the callers tell apart.
var (
	ErrExists      = errors.New("a site with this name already exists")
	ErrNotFound    = errors.New("there is no such site file")
	ErrNotEditable = errors.New("this site file was changed by hand, so it can't be changed here")
)

// Store works on the sites.d folder of one main configuration file.
type Store struct {
	Config string // path of the main configuration file
	mu     sync.Mutex
}

var (
	nameRe  = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)
	aliasRe = regexp.MustCompile(`^(\*\.)?[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)
	indexRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// Dir is the folder that holds the site files.
func (s *Store) Dir() string { return filepath.Join(filepath.Dir(s.Config), "sites.d") }

// File is where the site called name is kept.
func (s *Store) File(name string) string { return filepath.Join(s.Dir(), name+".conf") }

// DefaultRoot is the web directory a new site gets when none is given: C:/www/<name> on Windows, /var/www/<name>
// elsewhere. (The default site is in the public_html folder next to them.)
func DefaultRoot(name string) string {
	if runtime.GOOS == "windows" {
		return "C:/www/" + name
	}
	return "/var/www/" + name
}

// IncludeHint is a note for a main configuration that does not read the folder yet, or "".
func (s *Store) IncludeHint() string {
	data, err := os.ReadFile(s.Config)
	if err != nil || strings.Contains(string(data), "sites.d") {
		return ""
	}
	return s.Config + " does not read the sites.d folder yet. Add this line inside its server { } block: " + IncludeLine
}

// Normalize lower-cases and trims a spec and fills the defaults. It does not check it.
func Normalize(sp Spec) Spec {
	sp.Name = strings.ToLower(strings.TrimSpace(sp.Name))
	for i := range sp.Aliases {
		sp.Aliases[i] = strings.ToLower(strings.TrimSpace(sp.Aliases[i]))
	}
	sp.Aliases = nonEmpty(sp.Aliases)
	sp.Index = nonEmpty(sp.Index)
	// A pasted Windows path uses backslashes; filepath.ToSlash would leave them on Linux, so replace them here.
	sp.Root = strings.ReplaceAll(strings.TrimSpace(sp.Root), `\`, "/")
	if sp.Root == "" && sp.Name != "" {
		sp.Root = DefaultRoot(sp.Name)
	}
	if len(sp.Index) == 0 && !isPHP(sp.Template) {
		sp.Index = []string{"index.html"}
	}
	if sp.TLS == "" {
		sp.TLS = "off"
	}
	normalizeSettings(&sp)
	return sp
}

func nonEmpty(in []string) []string {
	var out []string
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// Validate checks a normalized spec. The checks are strict because the values are written into a file.
func Validate(sp Spec) error {
	if sp.Name == ServerFile {
		return fmt.Errorf("the name %q is reserved for the server settings file", sp.Name)
	}
	if len(sp.Name) > 253 || !nameRe.MatchString(sp.Name) {
		return fmt.Errorf("%q is not a host name (lower-case letters, digits, dots and dashes)", sp.Name)
	}
	if len(sp.Aliases) > 50 {
		return errors.New("at most 50 aliases")
	}
	seen := map[string]bool{sp.Name: true}
	for _, a := range sp.Aliases {
		if len(a) > 253 || !aliasRe.MatchString(a) {
			return fmt.Errorf("alias %q is not a host name (a leading *. is allowed)", a)
		}
		if seen[a] {
			return fmt.Errorf("alias %q is listed twice or repeats the name", a)
		}
		seen[a] = true
	}
	if err := checkPath(sp.Root); err != nil {
		return err
	}
	if len(sp.Index) > 10 {
		return errors.New("at most 10 index files")
	}
	for _, f := range sp.Index {
		if !indexRe.MatchString(f) || len(f) > 100 {
			return fmt.Errorf("index file %q: use a file name without a folder", f)
		}
	}
	if sp.TLS != "off" && sp.TLS != "automatic" {
		return errors.New(`tls must be "off" or "automatic"`)
	}
	return validateSettings(sp)
}

// checkPath accepts an absolute path that can be written inside a quoted string of the configuration.
func checkPath(p string) error {
	if p == "" || len(p) > 500 {
		return errors.New("the web directory is empty or too long")
	}
	if !(strings.HasPrefix(p, "/") || (len(p) > 2 && p[1] == ':' && p[2] == '/')) {
		return fmt.Errorf("the web directory %q must be an absolute path (such as /var/www/site or C:/www/site)", p)
	}
	if strings.ContainsAny(p, "\"\\${}\r\n\t\x00") {
		return errors.New("the web directory may not contain quotes, backslashes, $, braces or control characters (use / between folders)")
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return errors.New("the web directory may not contain a .. folder")
		}
	}
	return nil
}

// Render is the text of a site file for a normalized, valid spec.
func Render(sp Spec) string { return render(sp) }

func quoteList(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = fmt.Sprintf("%q", s)
	}
	return "[ " + strings.Join(q, ", ") + " ]"
}

// Parse reads a file in the layout Render writes (or the older layout without the spec line). ok is false for
// anything else, such as a file edited by hand.
func Parse(text string) (Spec, bool) {
	if sp, ok := parseV2(text); ok {
		return sp, true
	}
	return parseLegacy(text)
}

// parseLegacy reads the first layout: the marker, then site { aliases root index tls }. Saving upgrades it.
func parseLegacy(text string) (sp Spec, ok bool) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != Marker {
		return Spec{}, false
	}
	state := 0 // 0 before the site line, 1 inside, 2 after the closing brace
	for _, raw := range lines[1:] {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		switch {
		case state == 0 && strings.HasPrefix(line, "site ") && strings.HasSuffix(line, " {"):
			name, good := unquote(strings.TrimSuffix(strings.TrimPrefix(line, "site "), " {"))
			if !good {
				return Spec{}, false
			}
			sp.Name, state = name, 1
		case state == 1 && line == "}":
			state = 2
		case state == 1 && strings.HasPrefix(line, "aliases "):
			list, good := unquoteList(strings.TrimPrefix(line, "aliases "))
			if !good {
				return Spec{}, false
			}
			sp.Aliases = list
		case state == 1 && strings.HasPrefix(line, "index "):
			list, good := unquoteList(strings.TrimPrefix(line, "index "))
			if !good {
				return Spec{}, false
			}
			sp.Index = list
		case state == 1 && strings.HasPrefix(line, "root "):
			root, good := unquote(strings.TrimPrefix(line, "root "))
			if !good {
				return Spec{}, false
			}
			sp.Root = root
		case state == 1 && strings.HasPrefix(line, "tls "):
			sp.TLS = strings.TrimPrefix(line, "tls ")
		default:
			return Spec{}, false
		}
	}
	if state != 2 || Validate(Normalize(sp)) != nil {
		return Spec{}, false
	}
	return Normalize(sp), true
}

func unquote(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' || strings.Contains(s[1:len(s)-1], `"`) || strings.Contains(s, `\`) {
		return "", false
	}
	return s[1 : len(s)-1], true
}

func unquoteList(s string) ([]string, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil, false
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return nil, true
	}
	var out []string
	for _, part := range strings.Split(inner, ",") {
		v, ok := unquote(part)
		if !ok {
			return nil, false
		}
		out = append(out, v)
	}
	return out, true
}

// List returns the site files of the folder, sorted by name. A missing folder is an empty list.
func (s *Store) List() ([]Entry, error) {
	files, err := filepath.Glob(filepath.Join(s.Dir(), "*.conf"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	out := make([]Entry, 0, len(files))
	for _, f := range files {
		if filepath.Base(f) == ServerFile+".conf" {
			continue // the server settings file is not a site
		}
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		e := Entry{File: f}
		if sp, ok := Parse(string(data)); ok {
			e.Spec, e.Editable = sp, true
		} else {
			e.Spec = Spec{Name: strings.TrimSuffix(filepath.Base(f), ".conf")}
		}
		out = append(out, e)
	}
	return out, nil
}

// Add makes the site file for sp. With createRoot the web directory is made, with a sample page when the index is
// index.html and the folder has none.
func (s *Store) Add(sp Spec, createRoot bool) (Spec, error) {
	sp = Normalize(sp)
	if err := Validate(sp); err != nil {
		return sp, &ValidationError{Msg: err.Error()}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file := s.File(sp.Name)
	if _, err := os.Stat(file); err == nil {
		return sp, ErrExists
	}
	if err := s.write(file, Render(sp), nil); err != nil {
		return sp, err
	}
	if createRoot {
		makeRoot(sp)
	}
	return sp, nil
}

// Update replaces the site called name with sp (the name itself can't change).
func (s *Store) Update(name string, sp Spec) (Spec, error) {
	sp.Name = name
	sp = Normalize(sp)
	if err := Validate(sp); err != nil {
		return sp, &ValidationError{Msg: err.Error()}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file := s.File(sp.Name)
	old, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return sp, ErrNotFound
	}
	if err != nil {
		return sp, err
	}
	if _, ok := Parse(string(old)); !ok {
		return sp, ErrNotEditable
	}
	return sp, s.write(file, Render(sp), old)
}

// Remove deletes the site file. The web directory is not touched.
func (s *Store) Remove(name string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if !nameRe.MatchString(name) || name == ServerFile {
		return ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file := s.File(name)
	old, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := os.Remove(file); err != nil {
		return err
	}
	if res, diags := config.Check(s.Config, loader.Options{}); res == nil { // removing a site can break another that includes it
		_ = os.WriteFile(file, old, 0o644)
		return &InvalidError{Diagnostics: diags}
	}
	return nil
}

// Restore puts a file back as it was (old nil: removes it). The admin uses it when a reload is refused.
func (s *Store) Restore(name string, old []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file := s.File(name)
	if old == nil {
		_ = os.Remove(file)
		return
	}
	_ = os.WriteFile(file, old, 0o644)
}

// Read returns the current text of a site file, or nil when there is none.
func (s *Store) Read(name string) []byte {
	data, err := os.ReadFile(s.File(name))
	if err != nil {
		return nil
	}
	return data
}

// write replaces file with text through a temporary file, then checks the whole configuration. If it is not valid,
// old (nil: no file) is put back.
func (s *Store) write(file, text string, old []byte) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".site-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpName, file); err != nil {
		return err
	}
	if res, diags := config.Check(s.Config, loader.Options{}); res == nil {
		if old == nil {
			_ = os.Remove(file)
		} else {
			_ = os.WriteFile(file, old, 0o644)
		}
		return &InvalidError{Diagnostics: diags}
	}
	return nil
}

// makeRoot makes the web directory and, if it has no index page, a sample one. A proxy site has no files.
func makeRoot(sp Spec) {
	if sp.Template == "proxy" {
		return
	}
	dir := filepath.FromSlash(DocRoot(sp))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	if isPHP(sp.Template) {
		if oneOf(sp.Template, "wordpress", "drupal", "joomla") {
			return // the person puts the application here
		}
		page := filepath.Join(dir, "index.php")
		if _, err := os.Stat(page); err != nil {
			body := "<?php\necho '<h1>" + sp.Name + "</h1><p>PHP ' . PHP_VERSION . ' is served by LOTUS.</p>';\n"
			_ = os.WriteFile(page, []byte(body), 0o644)
		}
		return
	}
	for _, name := range sp.Index {
		if name != "index.html" {
			continue
		}
		page := filepath.Join(dir, "index.html")
		if _, err := os.Stat(page); err != nil {
			body := "<!doctype html>\n<meta charset=\"utf-8\">\n<title>" + sp.Name + "</title>\n<h1>" + sp.Name + "</h1>\n<p>This site is served by LOTUS.</p>\n"
			_ = os.WriteFile(page, []byte(body), 0o644)
		}
	}
}
