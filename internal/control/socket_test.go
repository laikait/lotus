package control

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestListenAndServeOverARealSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "control.sock")
	ln, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(Options{Backend: &fakeBackend{}, ServerName: "lotus/test", PID: os.Getpid()})
	done := make(chan error, 1)
	go func() { done <- s.Serve(ln) }()

	c, err := Dial(path, "test")
	if err != nil {
		t.Fatal(err)
	}
	var st Status
	if err := c.Call(MethodStatus, nil, &st); err != nil || st.PID != 42 {
		t.Errorf("%v %+v", err, st)
	}
	c.Close()

	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := <-done; err != nil {
		t.Errorf("Serve returned %v after Close", err)
	}
	if _, err := os.Lstat(path); err == nil {
		t.Error("closing the server must remove the socket file")
	}
	if _, err := Dial(path, "test"); !errors.Is(err, ErrUnreachable) {
		t.Errorf("after Close: %v", err)
	}
}

func TestSocketIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permissions are ACLs on Windows")
	}
	path := filepath.Join(t.TempDir(), "d", "control.sock")
	ln, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("socket mode = %04o, want 0600", fi.Mode().Perm())
	}
	di, _ := os.Stat(filepath.Dir(path))
	if di.Mode().Perm()&0o027 != 0 {
		t.Errorf("a directory created for the socket must not be open to others: %04o", di.Mode().Perm())
	}
}

func TestListenRefusesAWritableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory modes don't apply on Windows")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	_, err := Listen(filepath.Join(dir, "control.sock"))
	if err == nil || !strings.Contains(err.Error(), "writable by other users") {
		t.Errorf("err = %v", err)
	}
	// With the sticky bit (like /tmp) it is acceptable.
	if err := os.Chmod(dir, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	ln, err := Listen(filepath.Join(dir, "control.sock"))
	if err != nil {
		t.Errorf("sticky directory: %v", err)
	} else {
		ln.Close()
	}
}

func TestListenRemovesAStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	first, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	first.(*net.UnixListener).SetUnlinkOnClose(false) // what a crash leaves behind
	first.Close()
	if _, err := os.Lstat(path); err != nil {
		t.Skip("this platform removed the socket file anyway")
	}
	ln, err := Listen(path)
	if err != nil {
		t.Fatalf("a stale socket must be replaced: %v", err)
	}
	ln.Close()
}

func TestListenRefusesALiveSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	live, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	go func() {
		for {
			c, err := live.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if _, err := Listen(path); err == nil || !strings.Contains(err.Error(), "already listening") {
		t.Errorf("err = %v", err)
	}
	if _, err := Dial(path, "x"); err == nil {
		// the fake accept loop closes at once, so the handshake fails: that's fine
		t.Log("handshake unexpectedly worked")
	}
}

func TestListenDoesNotTouchOtherFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	if err := os.WriteFile(path, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Listen(path)
	if err == nil || !strings.Contains(err.Error(), "not a socket") {
		t.Errorf("err = %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "precious" {
		t.Errorf("an existing regular file was changed: %q", b)
	}
}

func TestListenNeedsAnAbsolutePath(t *testing.T) {
	if _, err := Listen("control.sock"); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("err = %v", err)
	}
}

func TestDefaultSocketPath(t *testing.T) {
	p := DefaultSocketPath()
	if !filepath.IsAbs(p) || filepath.Base(p) != "control.sock" {
		t.Errorf("default = %q", p)
	}
}

func TestDialReportsAnUnreachableServer(t *testing.T) {
	_, err := Dial(filepath.Join(t.TempDir(), "nothing.sock"), "x")
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("err = %v", err)
	}
}
