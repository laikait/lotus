//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSystemdInstallAndUninstall(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd is Linux only")
	}
	old := systemdPath
	systemdPath = filepath.Join(t.TempDir(), "lotus.service")
	defer func() { systemdPath = old }()
	if code, out, errOut := runCLI("service", "install", "--binary", "/opt/lotus"); code != exitOK || !strings.Contains(out, "systemctl daemon-reload") {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	if data, _ := os.ReadFile(systemdPath); !strings.Contains(string(data), "ExecStart=/opt/lotus start") {
		t.Errorf("%s", data)
	}
	if code, _, errOut := runCLI("service", "install"); code != exitUsage || !strings.Contains(errOut, "--force") {
		t.Errorf("must not overwrite: %d %s", code, errOut)
	}
	if code, _, _ := runCLI("service", "install", "--force"); code != exitOK {
		t.Errorf("--force: %d", code)
	}
	if code, _, _ := runCLI("service", "uninstall"); code != exitOK {
		t.Errorf("uninstall: %d", code)
	}
	if code, _, _ := runCLI("service", "uninstall"); code != exitNotFound {
		t.Errorf("second uninstall: %d", code)
	}
}
