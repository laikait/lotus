package phpinstall

import (
	"os"
	"path/filepath"
)

// RuntimeURL is where the person gets the Visual C++ runtime that PHP for Windows needs.
const RuntimeURL = "https://aka.ms/vs/17/release/vc_redist.x64.exe"

// RuntimeCheck looks for the Visual C++ runtime DLLs in the system folder. Without them php-cgi.exe does not start.
func RuntimeCheck() error {
	sys := filepath.Join(os.Getenv("SystemRoot"), "System32")
	for _, dll := range []string{"vcruntime140.dll", "vcruntime140_1.dll"} {
		if _, err := os.Stat(filepath.Join(sys, dll)); err != nil {
			return &RuntimeError{Missing: dll}
		}
	}
	return nil
}
