//go:build !windows

package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

func serviceInstall(p serviceParams, force bool, stdout, stderr io.Writer) int {
	if runtime.GOOS == "linux" {
		return serviceInstallLinux(p, force, stdout, stderr)
	}
	fmt.Fprintf(stderr, "lotus: service install is not available on %s; use `lotus service unit` and put its output where your system wants it\n", runtime.GOOS)
	return exitNotSupported
}

func serviceUninstall(stdout, stderr io.Writer) int {
	if runtime.GOOS == "linux" {
		return serviceUninstallLinux(stdout, stderr)
	}
	fmt.Fprintf(stderr, "lotus: service uninstall is not available on %s\n", runtime.GOOS)
	return exitNotSupported
}

// runAsService reports false: only Windows has a service manager that starts
// programs in a special way.
func runAsService(args []string, stdout, stderr io.Writer) (int, bool) { return 0, false }

// serviceLogPath is where the Windows service writes its log; there is no such file elsewhere (the system's own
// logging, such as journald, keeps the server's output).
func serviceLogPath() string { return "" }

var systemdPath = "/etc/systemd/system/lotus.service"

func serviceInstallLinux(p serviceParams, force bool, stdout, stderr io.Writer) int {
	if _, err := os.Stat(systemdPath); err == nil && !force {
		fmt.Fprintf(stderr, "lotus: %s exists; use --force to overwrite it\n", systemdPath)
		return exitUsage
	}
	if err := os.WriteFile(systemdPath, []byte(systemdUnit(p)), 0o644); err != nil {
		fmt.Fprintf(stderr, "lotus: %v (installing a service needs root)\n", err)
		return exitRuntime
	}
	fmt.Fprintf(stdout, "wrote %s\n", systemdPath)
	if !strings.HasPrefix(p.User, "root") {
		fmt.Fprintf(stdout, "create the account if it does not exist:  useradd --system --no-create-home --shell /usr/sbin/nologin %s\n", p.User)
	}
	fmt.Fprintln(stdout, "then:  systemctl daemon-reload && systemctl enable --now lotus")
	return exitOK
}

func serviceUninstallLinux(stdout, stderr io.Writer) int {
	if err := os.Remove(systemdPath); err != nil {
		fmt.Fprintf(stderr, "lotus: %v\n", err)
		return exitNotFound
	}
	fmt.Fprintf(stdout, "removed %s\nthen:  systemctl daemon-reload   (stop the service first with: systemctl disable --now lotus)\n", systemdPath)
	return exitOK
}
