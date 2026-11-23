//go:build unix

package loader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckSecureUnix(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "a.lotus")
	if err := os.WriteFile(file, []byte("x 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkSecure(file); err != nil {
		t.Errorf("a file owned by the current user with mode 0644 should pass: %v", err)
	}

	if err := os.Chmod(file, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := checkSecure(file); err == nil || !strings.Contains(err.Error(), "writable by everyone") {
		t.Errorf("a world-writable file must be rejected, got %v", err)
	}

	if err := os.Chmod(file, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := checkSecure(file); err == nil || !strings.Contains(err.Error(), dir) {
		t.Errorf("a file in a world-writable directory must be rejected, got %v", err)
	}
}
