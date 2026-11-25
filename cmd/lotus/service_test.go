package main

import (
	"runtime"
	"strings"
	"testing"
)

func TestSystemdUnit(t *testing.T) {
	u, err := serviceUnit("linux", serviceParams{Config: "/etc/lotus/main.lotus", User: "www", Binary: "/usr/local/bin/lotus"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"User=www", "ExecStart=/usr/local/bin/lotus start -config /etc/lotus/main.lotus", "ExecReload=/usr/local/bin/lotus reload",
		"AmbientCapabilities=CAP_NET_BIND_SERVICE", "NoNewPrivileges=true", "ProtectSystem=strict", "RuntimeDirectory=lotus",
		"Restart=on-failure", "WantedBy=multi-user.target",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("the unit lacks %q:\n%s", want, u)
		}
	}
}

func TestRCScriptAndOtherSystems(t *testing.T) {
	u, err := serviceUnit("freebsd", serviceParams{Config: "/usr/local/etc/lotus/main.lotus", User: "lotus", Binary: "/usr/local/bin/lotus"})
	if err != nil || !strings.Contains(u, "lotus_enable") || !strings.Contains(u, "/usr/local/bin/lotus start -config") {
		t.Errorf("%v\n%s", err, u)
	}
	if _, err := serviceUnit("windows", serviceParams{}); err == nil || !strings.Contains(err.Error(), "service install") {
		t.Errorf("windows has no printable unit: %v", err)
	}
}

func TestServiceUnitCommand(t *testing.T) {
	code, out, errOut := runCLI("service", "unit", "--config", "/srv/x.lotus", "--user", "web", "--binary", "/opt/lotus")
	switch runtime.GOOS {
	case "linux":
		if code != exitOK || !strings.Contains(out, "start -config /srv/x.lotus") || !strings.Contains(out, "User=web") {
			t.Errorf("%d %s %s", code, out, errOut)
		}
	case "freebsd":
		if code != exitOK || !strings.Contains(out, "/srv/x.lotus") {
			t.Errorf("%d %s %s", code, out, errOut)
		}
	default:
		if code != exitNotSupported {
			t.Errorf("%d", code)
		}
	}
	if code, _, _ := runCLI("service"); code != exitUsage {
		t.Errorf("no subcommand: %d", code)
	}
	if code, _, _ := runCLI("service", "bogus"); code != exitUsage {
		t.Errorf("unknown subcommand: %d", code)
	}
}
