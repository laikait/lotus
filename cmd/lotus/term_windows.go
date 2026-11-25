//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// echoOff stops the console from showing what is typed and returns a function that turns it back on. When standard
// input is not a console (a pipe), it does nothing.
func echoOff() func() {
	h := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return func() {}
	}
	if err := windows.SetConsoleMode(h, mode&^windows.ENABLE_ECHO_INPUT); err != nil {
		return func() {}
	}
	return func() { _ = windows.SetConsoleMode(h, mode) }
}
