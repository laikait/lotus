package sitefile

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/laikait/lotus/internal/config"
	"github.com/laikait/lotus/internal/config/diag"
	"github.com/laikait/lotus/internal/config/loader"
)

// MaxFileSize is the largest configuration file the editor reads or writes.
const MaxFileSize = 256 << 10

// File is a configuration file the editor may open: the main file, or a .conf file of the sites.d folder.
type File struct {
	Name    string `json:"name"` // "main.conf" or "sites.d/example.com.conf"
	Size    int64  `json:"size"`
	Managed bool   `json:"managed"` // made by "lotus site" or the Sites page, and still in its layout
}

var siteFileRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.conf$`)

// ErrBadName means the name is not one of the files the editor works on.
var ErrBadName = errors.New("this is not a file the editor can open")

// ErrTooBig and ErrBadContent are for text that can't be saved.
var (
	ErrTooBig     = errors.New("the file is too big for the editor (256 KiB at most)")
	ErrBadContent = errors.New("the text must be UTF-8 and may not contain a NUL character")
)

// path maps an editor name to a path, and refuses anything else. Only the main file and files directly in
// sites.d can be named, so no path, drive or ".." gets through.
func (s *Store) path(name string) (string, error) {
	if name == filepath.Base(s.Config) {
		return s.Config, nil
	}
	if rest, ok := strings.CutPrefix(name, "sites.d/"); ok && siteFileRe.MatchString(rest) {
		return filepath.Join(s.Dir(), rest), nil
	}
	return "", ErrBadName
}

// Files lists what the editor can open: the main file first, then the site files by name.
func (s *Store) Files() ([]File, error) {
	var out []File
	fi, err := os.Stat(s.Config)
	if err != nil {
		return nil, err
	}
	out = append(out, File{Name: filepath.Base(s.Config), Size: fi.Size()})
	entries, err := s.List()
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].File < entries[j].File })
	if st, err := os.Stat(s.File(ServerFile)); err == nil { // the server settings file is not a site, but it can be edited
		out = append(out, File{Name: "sites.d/" + ServerFile + ".conf", Size: st.Size()})
	}
	for _, e := range entries {
		base := filepath.Base(e.File)
		if !siteFileRe.MatchString(base) {
			continue
		}
		size := int64(0)
		if st, err := os.Stat(e.File); err == nil {
			size = st.Size()
		}
		out = append(out, File{Name: "sites.d/" + base, Size: size, Managed: e.Editable})
	}
	return out, nil
}

// ReadFile returns the text of one editable file.
func (s *Store) ReadFile(name string) (string, error) {
	p, err := s.path(name)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if fi.Size() > MaxFileSize {
		return "", ErrTooBig
	}
	data, err := os.ReadFile(p)
	return string(data), err
}

func checkContent(content string) error {
	if len(content) > MaxFileSize {
		return ErrTooBig
	}
	if !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		return ErrBadContent
	}
	return nil
}

// CheckFile says whether the whole configuration would be valid with content in the file. Nothing is kept: the
// file is put back as it was before this returns. The file is really replaced for that moment, because includes
// are resolved from where they are.
func (s *Store) CheckFile(name, content string) ([]diag.Diagnostic, error) {
	p, err := s.path(name)
	if err != nil {
		return nil, err
	}
	if err := checkContent(content); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	if err := atomicWrite(p, []byte(content)); err != nil {
		return nil, err
	}
	res, diags := config.Check(s.Config, loader.Options{})
	_ = atomicWrite(p, old)
	if res == nil {
		return diags, &InvalidError{Diagnostics: diags}
	}
	return diags, nil
}

// SaveFile replaces the file with content if the whole configuration stays valid, keeping the old text next to it
// as NAME.bak (a .bak file is not read as configuration). It returns the old text, so the caller can put it back
// with RestoreFile when the server refuses the new configuration.
func (s *Store) SaveFile(name, content string) (old []byte, err error) {
	p, err := s.path(name)
	if err != nil {
		return nil, err
	}
	if err := checkContent(content); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, err = os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	if err := atomicWrite(p, []byte(content)); err != nil {
		return nil, err
	}
	if res, diags := config.Check(s.Config, loader.Options{}); res == nil {
		_ = atomicWrite(p, old)
		return nil, &InvalidError{Diagnostics: diags}
	}
	_ = os.WriteFile(p+".bak", old, 0o644)
	return old, nil
}

// RestoreFile puts a file back as it was.
func (s *Store) RestoreFile(name string, old []byte) {
	p, err := s.path(name)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = atomicWrite(p, old)
}

// atomicWrite replaces path with data through a temporary file in the same folder.
func atomicWrite(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".edit-*")
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
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, path)
}
