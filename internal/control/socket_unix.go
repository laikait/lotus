//go:build !windows

package control

import (
	"fmt"
	"os"
	"syscall"
)

// checkDirectory refuses a socket directory that someone else could write to
// (they could swap the socket for their own) or that another user owns.
func checkDirectory(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("control socket directory: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("control socket directory %s is not a directory", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		if uid := int(st.Uid); uid != os.Geteuid() && uid != 0 {
			return fmt.Errorf("control socket directory %s is owned by user %d; it must be owned by root or the user running lotus", dir, uid)
		}
	}
	// Group- or world-writable is fine only with the sticky bit (like /tmp).
	if fi.Mode().Perm()&0o022 != 0 && fi.Mode()&os.ModeSticky == 0 {
		return fmt.Errorf("control socket directory %s is writable by other users (mode %04o); run: chmod go-w %s",
			dir, fi.Mode().Perm(), dir)
	}
	return nil
}

func restrictSocket(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("control socket permissions: %w", err)
	}
	return nil
}
