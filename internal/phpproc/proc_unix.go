//go:build !windows

package phpproc

import (
	"os"
	"syscall"
)

// procAttr puts each process in its own process group, so a Ctrl-C meant for
// the server doesn't reach them first: the server stops them itself, after the
// running requests are done.
func procAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// confine has nothing to do on Unix. (A parent-death signal would be the
// equivalent, but Go delivers it when the OS thread that started the process
// exits, which can happen at any time.)
func confine(*os.Process) {}
