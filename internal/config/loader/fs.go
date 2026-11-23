package loader

import (
	"io"
	"os"
	"path/filepath"
	"sort"
)

// MaxFileSize is the largest configuration file the loader reads.
const MaxFileSize = 16 << 20

// FS is the file system access the loader needs. It exists so tests can use
// an in-memory implementation.
type FS interface {
	// ReadFile returns the contents of a file (at most MaxFileSize bytes).
	ReadFile(name string) ([]byte, error)
	// Glob returns the files matching a pattern, sorted by byte order.
	Glob(pattern string) ([]string, error)
	// Canonical returns an absolute, cleaned path with symbolic links resolved,
	// used to detect include cycles.
	Canonical(name string) (string, error)
	// CheckSecure reports whether an included file and its directory are safe
	// to trust (grammar §6.5). It returns nil where the platform has no check.
	CheckSecure(name string) error
}

// OS is the real file system.
type OS struct{}

// ReadFile implements FS.
func (OS) ReadFile(name string) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxFileSize+1))
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Glob implements FS.
func (OS) Glob(pattern string) ([]string, error) {
	m, err := filepath.Glob(pattern)
	sort.Strings(m)
	return m, err
}

// Canonical implements FS.
func (OS) Canonical(name string) (string, error) {
	abs, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
}

// CheckSecure implements FS.
func (OS) CheckSecure(name string) error { return checkSecure(name) }
