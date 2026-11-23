//go:build unix

package fspath

import (
	"errors"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Opening a FIFO blocks until a writer appears, so a request for one could
// hang a goroutine forever. The resolver must refuse it without opening it.
func TestFIFOIsRefusedWithoutBlocking(t *testing.T) {
	root, _ := site(t)
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o644); err != nil {
		t.Skipf("cannot create a FIFO: %v", err)
	}
	r := resolver(t, root, Policy{})
	done := make(chan error, 1)
	go func() {
		f, err := r.Open("/pipe")
		if err == nil {
			f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrForbidden) {
			t.Errorf("error = %v, want ErrForbidden", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Open blocked on a FIFO")
	}
}
