//go:build linux

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// echoOff stops the terminal from showing what is typed and returns a function that turns it back on. When
// standard input is not a terminal (a pipe), it does nothing.
func echoOff() func() {
	fd := int(os.Stdin.Fd())
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return func() {}
	}
	quiet := *old
	quiet.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &quiet); err != nil {
		return func() {}
	}
	return func() { _ = unix.IoctlSetTermios(fd, unix.TCSETS, old) }
}
