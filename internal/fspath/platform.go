package fspath

import (
	"runtime"
	"strings"
)

// platformHazard reports whether a single path segment is dangerous on this
// operating system. On Windows that covers device names (CON, NUL, COM1, ...),
// alternate data streams ("file::$DATA"), and names that Windows silently
// rewrites (trailing dots and spaces, 8.3 short-name tildes). Elsewhere only
// NUL bytes matter, and those are rejected earlier.
func platformHazard(seg string) bool {
	return runtime.GOOS == "windows" && windowsHazard(seg)
}

var reservedWindowsNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true, "conin$": true, "conout$": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// windowsHazard is the Windows rule set. It is separate from platformHazard
// so it can be tested on every platform.
func windowsHazard(seg string) bool {
	if strings.ContainsAny(seg, `:<>"|?*`) {
		return true // streams, and characters Windows file names can't contain
	}
	if strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " ") {
		return true // Windows strips these, so "a." and "a" would be the same file
	}
	for _, r := range seg {
		if r < 0x20 {
			return true
		}
	}
	// 8.3 short names such as "PROGRA~1" reach the same file under another name.
	if i := strings.IndexByte(seg, '~'); i >= 0 && i+1 < len(seg) && seg[i+1] >= '0' && seg[i+1] <= '9' {
		return true
	}
	// Device names are reserved with or without an extension ("nul.txt").
	base := seg
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	return reservedWindowsNames[strings.ToLower(strings.TrimRight(base, " "))]
}
