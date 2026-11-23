//go:build unix

package loader

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// checkSecure rejects an included file, or the directory holding it, that is
// writable by everyone or owned by someone other than root or the current user.
func checkSecure(name string) error {
	for _, p := range []string{name, filepath.Dir(name)} {
		fi, err := os.Stat(p)
		if err != nil {
			return err
		}
		if fi.Mode().Perm()&0o002 != 0 {
			return fmt.Errorf("%s is writable by everyone", p)
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			uid := int(st.Uid)
			if uid != 0 && uid != os.Getuid() {
				return fmt.Errorf("%s is owned by uid %d (it must be owned by root or the user running lotus)", p, uid)
			}
		}
	}
	return nil
}
