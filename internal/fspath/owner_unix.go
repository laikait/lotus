//go:build unix

package fspath

import (
	"io/fs"
	"syscall"
)

// owner returns the numeric owner of a file.
func owner(fi fs.FileInfo) (uint64, bool) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Uid), true
	}
	return 0, false
}
