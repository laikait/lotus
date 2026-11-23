package loader

import (
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// MemFS is an in-memory FS for tests. Paths use forward slashes.
type MemFS struct {
	// Files maps a path to its contents.
	Files map[string]string
	// Unsafe maps a path to the reason CheckSecure rejects it.
	Unsafe map[string]string
}

func memPath(name string) string { return path.Clean(filepath.ToSlash(name)) }

// ReadFile implements FS.
func (m MemFS) ReadFile(name string) ([]byte, error) {
	if s, ok := m.Files[memPath(name)]; ok {
		return []byte(s), nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// Glob implements FS.
func (m MemFS) Glob(pattern string) ([]string, error) {
	pat := memPath(pattern)
	var out []string
	for name := range m.Files {
		ok, err := path.Match(pat, name)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Canonical implements FS.
func (m MemFS) Canonical(name string) (string, error) { return memPath(name), nil }

// CheckSecure implements FS.
func (m MemFS) CheckSecure(name string) error {
	if why, bad := m.Unsafe[memPath(name)]; bad {
		return fmt.Errorf("%s", why)
	}
	return nil
}

// String lists the files, for test failure messages.
func (m MemFS) String() string {
	names := make([]string, 0, len(m.Files))
	for n := range m.Files {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
