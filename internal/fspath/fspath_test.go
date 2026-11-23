package fspath

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// site builds a document root:
//
//	root/index.html, root/a/b/c.txt, root/.env, root/.well-known/acme, root/sub/.git/config
//
// and, next to it, outside/secret.txt (which must never be reachable).
func site(t *testing.T) (root, outside string) {
	t.Helper()
	base := t.TempDir()
	root, outside = filepath.Join(base, "root"), filepath.Join(base, "outside")
	files := map[string]string{
		filepath.Join(root, "index.html"):            "home",
		filepath.Join(root, "a", "b", "c.txt"):       "deep",
		filepath.Join(root, ".env"):                  "SECRET=1",
		filepath.Join(root, ".well-known", "acme"):   "token",
		filepath.Join(root, "sub", ".git", "config"): "[core]",
		filepath.Join(root, "sub", "page.html"):      "page",
		filepath.Join(outside, "secret.txt"):         "TOP SECRET",
		filepath.Join(outside, "dir", "inner.txt"):   "inner",
	}
	for p, content := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, outside
}

func open(t *testing.T, r *Resolver, path string) (*File, error) {
	t.Helper()
	f, err := r.Open(path)
	if err == nil {
		t.Cleanup(func() { f.Close() })
	}
	return f, err
}

func resolver(t *testing.T, root string, p Policy) *Resolver {
	t.Helper()
	r, err := New(root, p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func TestSegments(t *testing.T) {
	tests := []struct {
		in   string
		want string // joined with "|"
		err  error
	}{
		{"/", "", nil},
		{"/a/b/c", "a|b|c", nil},
		{"//a///b/", "a|b", nil},
		{"/a/./b", "a|b", nil},
		{"/a/../b", "", ErrBadPath},
		{"/..", "", ErrBadPath},
		{"/a/..", "", ErrBadPath},
		{"a/b", "", ErrBadPath},
		{"", "", ErrBadPath},
		{"/a\x00b", "", ErrBadPath},
		{`/a\b`, "", ErrBadPath},
		{`/..\secret`, "", ErrBadPath},
		{"/...", "...", nil}, // three dots is an ordinary (if odd) name
	}
	for _, tt := range tests {
		got, err := Segments(tt.in)
		if !errors.Is(err, tt.err) {
			t.Errorf("Segments(%q) error = %v, want %v", tt.in, err, tt.err)
			continue
		}
		if err == nil && strings.Join(got, "|") != tt.want {
			t.Errorf("Segments(%q) = %v, want %q", tt.in, got, tt.want)
		}
	}
}

func TestOpen(t *testing.T) {
	root, _ := site(t)
	r := resolver(t, root, Policy{Symlinks: SymlinksDeny})

	tests := []struct {
		path  string
		err   error
		isDir bool
		rel   string
	}{
		{"/index.html", nil, false, "index.html"},
		{"/", nil, true, ""},
		{"/a/b/c.txt", nil, false, "a/b/c.txt"},
		{"/a/b", nil, true, "a/b"},
		{"//a///b//c.txt", nil, false, "a/b/c.txt"},
		{"/a/./b/c.txt", nil, false, "a/b/c.txt"},
		{"/missing", ErrNotFound, false, ""},
		{"/a/missing/c.txt", ErrNotFound, false, ""},
		{"/index.html/more", ErrNotFound, false, ""}, // a file in the middle of the path
		{"/a/../index.html", ErrBadPath, false, ""},
		{"/../../etc/passwd", ErrBadPath, false, ""},
		{"/%2e%2e/secret", ErrNotFound, false, ""}, // not decoded here: a literal name that doesn't exist
		{"index.html", ErrBadPath, false, ""},
		{"/a\x00.txt", ErrBadPath, false, ""},
		{`/a\b\c.txt`, ErrBadPath, false, ""},
		{"/.env", ErrForbidden, false, ""},
		{"/sub/.git/config", ErrForbidden, false, ""},
		{"/sub/.git", ErrForbidden, false, ""},
		{"/.well-known/acme", nil, false, ".well-known/acme"},
		{"/sub/.well-known/x", ErrForbidden, false, ""}, // only allowed as the first segment
		{"/.well-known/../.env", ErrBadPath, false, ""},
	}
	for _, tt := range tests {
		f, err := open(t, r, tt.path)
		if !errors.Is(err, tt.err) {
			t.Errorf("Open(%q) error = %v, want %v", tt.path, err, tt.err)
			continue
		}
		if err != nil {
			continue
		}
		if f.Info.IsDir() != tt.isDir || f.Rel != tt.rel {
			t.Errorf("Open(%q) = dir:%v rel:%q, want dir:%v rel:%q", tt.path, f.Info.IsDir(), f.Rel, tt.isDir, tt.rel)
		}
	}
}

func TestOpenReadsContent(t *testing.T) {
	root, _ := site(t)
	r := resolver(t, root, Policy{})
	f, err := open(t, r, "/a/b/c.txt")
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	n, _ := f.Read(buf)
	if string(buf[:n]) != "deep" {
		t.Errorf("content = %q", buf[:n])
	}
}

func TestDotfilePolicy(t *testing.T) {
	root, _ := site(t)
	r := resolver(t, root, Policy{AllowDotfiles: true})
	for _, p := range []string{"/.env", "/sub/.git/config", "/sub/.well-known"} {
		if _, err := open(t, r, p); p != "/sub/.well-known" && err != nil {
			t.Errorf("with AllowDotfiles, Open(%q) failed: %v", p, err)
		}
	}
	// Even when allowed, ".." is never resolved.
	if _, err := open(t, r, "/.git/../.env"); !errors.Is(err, ErrBadPath) {
		t.Errorf("error = %v, want ErrBadPath", err)
	}
}

func TestOpenNonexistentRoot(t *testing.T) {
	if _, err := New(filepath.Join(t.TempDir(), "nope"), Policy{}); err == nil {
		t.Error("opening a missing document root must fail")
	}
}

// symlink creates a link, skipping the test where the platform or the
// current privileges don't allow it (Windows needs a privilege or developer mode).
func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlinks here (%v); symlink behavior is covered on platforms that allow it", err)
	}
}

func TestSymlinkPolicies(t *testing.T) {
	root, outside := site(t)
	symlink(t, "index.html", filepath.Join(root, "inside-file")) // relative, inside, to a file
	symlink(t, "a", filepath.Join(root, "inside-dir"))           // relative, inside, to a directory
	// Absolute links are refused even when they point back inside the root.
	symlink(t, filepath.Join(root, "index.html"), filepath.Join(root, "absolute-inside"))
	symlink(t, "a/b", filepath.Join(root, "relative-dir"))                               // relative link, inside
	symlink(t, filepath.Join(outside, "secret.txt"), filepath.Join(root, "escape-file")) // absolute, outside
	symlink(t, filepath.Join(outside, "dir"), filepath.Join(root, "escape-dir"))         // directory, outside
	symlink(t, filepath.Join("..", "outside", "secret.txt"), filepath.Join(root, "escape-relative"))
	symlink(t, "escape-file", filepath.Join(root, "chain")) // link to a link that escapes

	sameOwnerOK := runtime.GOOS != "windows" // owner_match only follows where owners can be compared

	tests := []struct {
		name   string
		policy SymlinkPolicy
		path   string
		want   error
	}{
		{"deny: inside file link", SymlinksDeny, "/inside-file", ErrForbidden},
		{"deny: inside dir link", SymlinksDeny, "/inside-dir/b/c.txt", ErrForbidden},

		{"follow: inside file link", SymlinksFollow, "/inside-file", nil},
		{"follow: inside dir link", SymlinksFollow, "/inside-dir/b/c.txt", nil},
		{"follow: relative dir link", SymlinksFollow, "/relative-dir/c.txt", nil},
		{"follow: absolute link is refused even when it points inside", SymlinksFollow, "/absolute-inside", ErrForbidden},
		{"follow: absolute link outside the root", SymlinksFollow, "/escape-file", ErrForbidden},
		{"follow: directory link outside the root", SymlinksFollow, "/escape-dir/inner.txt", ErrForbidden},
		{"follow: relative link outside the root", SymlinksFollow, "/escape-relative", ErrForbidden},
		{"follow: chain ending outside the root", SymlinksFollow, "/chain", ErrForbidden},

		{"owner_match: escaping link is still refused", SymlinksOwnerMatch, "/escape-file", ErrForbidden},
		{"owner_match: escaping dir is still refused", SymlinksOwnerMatch, "/escape-dir/inner.txt", ErrForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := resolver(t, root, Policy{Symlinks: tt.policy})
			f, err := open(t, r, tt.path)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Open(%q) error = %v, want %v", tt.path, err, tt.want)
			}
			if err == nil {
				buf := make([]byte, 64)
				n, _ := f.Read(buf)
				if strings.Contains(string(buf[:n]), "TOP SECRET") {
					t.Fatal("served a file from outside the document root")
				}
			}
		})
	}

	t.Run("owner_match: same owner is followed where owners are comparable", func(t *testing.T) {
		r := resolver(t, root, Policy{Symlinks: SymlinksOwnerMatch})
		_, err := open(t, r, "/inside-file")
		if sameOwnerOK && err != nil {
			t.Errorf("a link and target with the same owner must be followed: %v", err)
		}
		if !sameOwnerOK && !errors.Is(err, ErrForbidden) {
			t.Errorf("where owners can't be compared, owner_match must refuse: %v", err)
		}
	})

	t.Run("a dangling link is not found, not a server error", func(t *testing.T) {
		symlink(t, "nowhere", filepath.Join(root, "dangling"))
		r := resolver(t, root, Policy{Symlinks: SymlinksFollow})
		if _, err := open(t, r, "/dangling"); !errors.Is(err, ErrNotFound) {
			t.Errorf("error = %v, want ErrNotFound", err)
		}
	})
}

func TestSymlinkDotfileCombination(t *testing.T) {
	root, _ := site(t)
	symlink(t, filepath.Join(root, "index.html"), filepath.Join(root, ".hidden-link"))
	r := resolver(t, root, Policy{Symlinks: SymlinksFollow})
	if _, err := open(t, r, "/.hidden-link"); !errors.Is(err, ErrForbidden) {
		t.Errorf("a dotfile symlink is still a dotfile: %v", err)
	}
}

func TestWindowsHazards(t *testing.T) {
	bad := []string{
		"con", "CON", "nul", "NUL.txt", "aux.tar.gz", "com1", "LPT9", "conin$",
		"file.txt::$DATA", "file.txt:stream", "name.", "name ", "a<b", "a>b", `a"b`, "a|b", "a?b", "a*b",
		"PROGRA~1", "docume~2.txt", "x\x01y",
	}
	for _, s := range bad {
		if !windowsHazard(s) {
			t.Errorf("windowsHazard(%q) = false, want true", s)
		}
	}
	good := []string{"index.html", "console.log", "connect", "com10", "lpt0", "a.b.c", "~backup", "tilde~x", "file-1.txt", "naïve.txt", "nulls"}
	for _, s := range good {
		if windowsHazard(s) {
			t.Errorf("windowsHazard(%q) = true, want false", s)
		}
	}
}

func TestPlatformHazardAppliesOnlyOnWindows(t *testing.T) {
	if got := platformHazard("con"); got != (runtime.GOOS == "windows") {
		t.Errorf("platformHazard(con) = %v on %s", got, runtime.GOOS)
	}
	// A name with a colon is legal on Unix and must stay reachable there.
	if runtime.GOOS != "windows" && platformHazard("a:b") {
		t.Error("colons are valid in Unix file names")
	}
}

func TestOpenRejectsHazardousNamesOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only behavior")
	}
	root, _ := site(t)
	r := resolver(t, root, Policy{})
	for _, p := range []string{"/nul", "/index.html::$DATA", "/index.html.", "/a/CON.txt", "/PROGRA~1"} {
		if _, err := open(t, r, p); !errors.Is(err, ErrForbidden) {
			t.Errorf("Open(%q) error = %v, want ErrForbidden", p, err)
		}
	}
}

func TestHtaccessFilesAreNeverServedButCanBeRead(t *testing.T) {
	root, _ := site(t)
	for _, f := range []string{".htaccess", ".htpasswd", "sub/.htaccess", "sub/.HTACCESS", "sub/.htsomething"} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(f)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, allowDot := range []bool{false, true} {
		r, err := New(root, Policy{Symlinks: SymlinksDeny, AllowDotfiles: allowDot})
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		for _, p := range []string{"/.htaccess", "/.htpasswd", "/sub/.htaccess", "/sub/.htsomething", "/sub/.HtAccess"} {
			if f, err := r.Open(p); !errors.Is(err, ErrForbidden) {
				if f != nil {
					f.Close()
				}
				t.Errorf("AllowDotfiles=%v: Open(%q) = %v, want forbidden", allowDot, p, err)
			}
		}
		// OpenHtaccess is how the server reads them.
		for _, dir := range []string{"/", "/sub/"} {
			f, err := r.OpenHtaccess(dir)
			if err != nil {
				t.Errorf("OpenHtaccess(%q): %v", dir, err)
				continue
			}
			f.Close()
		}
		if _, err := r.OpenHtaccess("/a/"); !errors.Is(err, ErrNotFound) {
			t.Errorf("a directory without the file: %v", err)
		}
		if _, err := r.OpenHtaccess("/nodir/"); !errors.Is(err, ErrNotFound) {
			t.Errorf("a missing directory: %v", err)
		}
	}
}

func TestOpenHtaccessFollowsTheSymlinkPolicy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	root, outside := site(t)
	if err := os.WriteFile(filepath.Join(outside, "evil-htaccess"), []byte("Require all granted"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A .htaccess that is a link to a file outside the root must not be read.
	if err := os.Symlink(filepath.Join(outside, "evil-htaccess"), filepath.Join(root, "sub", ".htaccess")); err != nil {
		t.Skip(err)
	}
	for _, pol := range []SymlinkPolicy{SymlinksDeny, SymlinksOwnerMatch, SymlinksFollow} {
		r, _ := New(root, Policy{Symlinks: pol})
		if f, err := r.OpenHtaccess("/sub/"); err == nil {
			f.Close()
			t.Errorf("policy %d: a .htaccess linked from outside the root was opened", pol)
		}
		r.Close()
	}
}

func TestOpenHtaccessIgnoresADirectoryOfThatName(t *testing.T) {
	root, _ := site(t)
	if err := os.MkdirAll(filepath.Join(root, "a", ".htaccess"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, _ := New(root, Policy{})
	defer r.Close()
	if _, err := r.OpenHtaccess("/a/"); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v", err)
	}
}
