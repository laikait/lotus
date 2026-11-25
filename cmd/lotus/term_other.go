//go:build !windows && !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package main

// echoOff does nothing on this system: a typed password is shown. Use --password-stdin to avoid that.
func echoOff() func() { return func() {} }
