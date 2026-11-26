//go:build !windows

package phpinstall

// RuntimeURL is where the person gets the Visual C++ runtime that PHP for Windows needs.
const RuntimeURL = "https://aka.ms/vs/17/release/vc_redist.x64.exe"

// RuntimeCheck has nothing to check outside Windows.
func RuntimeCheck() error { return nil }
