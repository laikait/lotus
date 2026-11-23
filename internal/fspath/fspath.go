// Package fspath is the one place where a URL path becomes a file. Every part
// of LOTUS that maps requests to files (static serving, script resolution,
// .htaccess discovery) must go through a Resolver, so that traversal, symlink
// and hidden-file rules are enforced in one reviewed place.
//
// Containment is enforced by the operating system through os.Root: even if a
// symlink is swapped while a request is being served, a path can't leave the
// document root. On top of that the Resolver applies LOTUS policy: symlink
// policy, dotfile policy and platform hazards (Windows device names, alternate
// data streams).
package fspath

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// Errors returned by Open. Callers map them to HTTP statuses: ErrNotFound is
// 404, ErrForbidden is 403, ErrBadPath is 400.
var (
	ErrNotFound  = errors.New("fspath: not found")
	ErrForbidden = errors.New("fspath: forbidden")
	ErrBadPath   = errors.New("fspath: bad path")
)

// SymlinkPolicy says how symbolic links below the root are treated.
type SymlinkPolicy int

// Symlink policies. The zero value is the strictest.
const (
	SymlinksDeny       SymlinkPolicy = iota // refuse every symlink
	SymlinksOwnerMatch                      // follow a link only if link and target have the same owner
	SymlinksFollow                          // follow links that stay inside the root
)

// Policy configures a Resolver.
type Policy struct {
	Symlinks SymlinkPolicy
	// AllowDotfiles permits path segments that start with a dot. Without it
	// they are refused, except a first segment of ".well-known".
	AllowDotfiles bool
}

// Resolver maps URL paths to files below one root directory.
type Resolver struct {
	root   *os.Root
	policy Policy
}

// New opens root as a document root.
func New(root string, p Policy) (*Resolver, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("opening document root: %w", err)
	}
	return &Resolver{root: r, policy: p}, nil
}

// Close releases the root directory.
func (r *Resolver) Close() error { return r.root.Close() }

// File is an opened file or directory.
type File struct {
	*os.File
	Info fs.FileInfo
	// Rel is the cleaned path below the root, with forward slashes ("" is the root).
	Rel string
}

// Segments validates a URL path and returns its segments. The path must start
// with "/". Empty and "." segments are dropped. ".." is an error: callers
// normalize paths before routing, so seeing one here means something is wrong.
func Segments(urlPath string) ([]string, error) {
	if !strings.HasPrefix(urlPath, "/") {
		return nil, fmt.Errorf("%w: must start with a slash", ErrBadPath)
	}
	if strings.ContainsRune(urlPath, 0) {
		return nil, fmt.Errorf("%w: contains NUL", ErrBadPath)
	}
	if strings.ContainsRune(urlPath, '\\') {
		return nil, fmt.Errorf("%w: contains backslash", ErrBadPath)
	}
	var segs []string
	for _, s := range strings.Split(urlPath, "/") {
		switch s {
		case "", ".":
		case "..":
			return nil, fmt.Errorf("%w: contains a dot-dot segment", ErrBadPath)
		default:
			segs = append(segs, s)
		}
	}
	return segs, nil
}

// checkSegments applies the dotfile rule and the platform rules.
func (r *Resolver) checkSegments(segs []string) error {
	for i, s := range segs {
		if strings.HasPrefix(s, ".") && !r.policy.AllowDotfiles && !(i == 0 && s == ".well-known") {
			return fmt.Errorf("%w: hidden file", ErrForbidden)
		}
		if platformHazard(s) {
			return fmt.Errorf("%w: unsafe name on this platform", ErrForbidden)
		}
	}
	return nil
}

// Open resolves urlPath below the root and opens it. The result is a regular
// file or a directory; anything else (devices, sockets, pipes) is refused.
// The caller must Close the file.
func (r *Resolver) Open(urlPath string) (*File, error) {
	segs, err := Segments(urlPath)
	if err != nil {
		return nil, err
	}
	if err := r.checkSegments(segs); err != nil {
		return nil, err
	}

	cur := ""
	for i, seg := range segs {
		if cur == "" {
			cur = seg
		} else {
			cur += "/" + seg
		}
		fi, err := r.root.Lstat(cur)
		if err != nil {
			return nil, mapErr(err)
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			if err := r.checkSymlink(cur, fi); err != nil {
				return nil, err
			}
			if fi, err = r.root.Stat(cur); err != nil { // follows the link; fails if it leaves the root
				return nil, mapErr(err)
			}
		}
		if i < len(segs)-1 && !fi.IsDir() {
			return nil, ErrNotFound // a file in the middle of the path
		}
	}

	rel := cur
	if rel == "" {
		rel = "."
	}
	// Check the type before opening: opening a FIFO could block forever.
	st, err := r.root.Stat(rel)
	if err != nil {
		return nil, mapErr(err)
	}
	if !st.Mode().IsRegular() && !st.IsDir() {
		return nil, fmt.Errorf("%w: not a regular file or directory", ErrForbidden)
	}
	f, err := r.root.Open(rel)
	if err != nil {
		return nil, mapErr(err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, mapErr(err)
	}
	if !info.Mode().IsRegular() && !info.IsDir() { // swapped between Stat and Open
		f.Close()
		return nil, fmt.Errorf("%w: not a regular file or directory", ErrForbidden)
	}
	return &File{File: f, Info: info, Rel: cur}, nil
}

func (r *Resolver) checkSymlink(rel string, link fs.FileInfo) error {
	switch r.policy.Symlinks {
	case SymlinksFollow:
		return nil
	case SymlinksOwnerMatch:
		target, err := r.root.Stat(rel)
		if err != nil {
			return mapErr(err)
		}
		lo, ok1 := owner(link)
		to, ok2 := owner(target)
		if ok1 && ok2 && lo == to {
			return nil
		}
		return fmt.Errorf("%w: symlink owner does not match its target", ErrForbidden)
	default:
		return fmt.Errorf("%w: symlinks are not allowed", ErrForbidden)
	}
}

// mapErr turns file system errors into this package's errors. Errors from
// os.Root about escaping the root mean the path is forbidden, not missing.
func mapErr(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ErrNotFound
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("%w: permission denied", ErrForbidden)
	}
	if strings.Contains(err.Error(), "escapes from parent") || strings.Contains(err.Error(), "path escapes") {
		return fmt.Errorf("%w: path leaves the document root", ErrForbidden)
	}
	// Other errors: not a directory, too many links, name too long, ...
	// None of them is a server fault the client can fix, so they read as 404.
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return ErrNotFound
	}
	return err
}
