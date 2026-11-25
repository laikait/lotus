package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

const serviceUsage = `usage: lotus service unit [--config FILE] [--user NAME] [--binary PATH]
       lotus service install [--config FILE] [--user NAME] [--binary PATH] [--force]
       lotus service uninstall

unit      prints the service definition for this system (a systemd unit on Linux, an
          rc.d script on FreeBSD)
install   Linux: writes /etc/systemd/system/lotus.service. Windows: registers the LOTUS
          service (starts at boot, restarts on failure)
uninstall removes what install made

install and uninstall need root (Linux) or an administrator (Windows). Neither starts
the service: they print the command that does.`

// cmdService is `lotus service ...`.
func cmdService(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, serviceUsage)
		return exitUsage
	}
	sub := args[0]
	fs := flag.NewFlagSet("lotus service "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg := fs.String("config", "", "configuration file (default "+defaultConfigPath()+")")
	user := fs.String("user", "lotus", "the account the server runs as (Linux and FreeBSD)")
	bin := fs.String("binary", "", "path of the lotus program (default: this one)")
	force := fs.Bool("force", false, "overwrite an existing service definition")
	fs.Usage = func() { fmt.Fprintln(stderr, serviceUsage) }
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		return exitUsage
	}
	p := serviceParams{Config: *cfg, User: *user, Binary: *bin}
	if p.Config == "" {
		p.Config = defaultConfigPath()
	}
	if p.Binary == "" {
		if exe, err := os.Executable(); err == nil {
			p.Binary = exe
		} else {
			p.Binary = "/usr/local/bin/lotus"
		}
	}
	if abs, err := filepath.Abs(p.Config); err == nil {
		p.Config = abs
	}
	switch sub {
	case "unit":
		text, err := serviceUnit(runtime.GOOS, p)
		if err != nil {
			fmt.Fprintf(stderr, "lotus: %v\n", err)
			return exitNotSupported
		}
		fmt.Fprint(stdout, text)
		return exitOK
	case "install":
		return serviceInstall(p, *force, stdout, stderr)
	case "uninstall":
		return serviceUninstall(stdout, stderr)
	}
	fmt.Fprintln(stderr, serviceUsage)
	return exitUsage
}

type serviceParams struct {
	Config, User, Binary string
}

// serviceUnit renders the service definition for an operating system.
func serviceUnit(goos string, p serviceParams) (string, error) {
	switch goos {
	case "linux":
		return systemdUnit(p), nil
	case "freebsd":
		return rcdScript(p), nil
	}
	return "", fmt.Errorf("there is no service definition to print on %s (on Windows use `lotus service install`)", goos)
}

// systemdUnit is a unit with the usual hardening. The server binds low ports
// through a capability, not by running as root; it can write only where it
// must (certificates, logs, the control socket).
func systemdUnit(p serviceParams) string {
	return fmt.Sprintf(`[Unit]
Description=LOTUS web server
Documentation=https://github.com/laikait/lotus
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=%[1]s
Group=%[1]s
ExecStart=%[2]s start -config %[3]s
ExecReload=%[2]s reload
ExecStop=%[2]s stop
Restart=on-failure
RestartSec=2s
TimeoutStopSec=40s
LimitNOFILE=1048576

# Listen on ports below 1024 without being root.
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE

# The control socket is /run/lotus/control.sock (the default).
RuntimeDirectory=lotus
RuntimeDirectoryMode=0750
StateDirectory=lotus
LogsDirectory=lotus

# Hardening. Add the document roots and log directories your sites write to
# with ReadWritePaths=; everything else is read-only.
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=read-only
PrivateTmp=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
RestrictRealtime=true
LockPersonality=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
SystemCallArchitectures=native
UMask=0027

[Install]
WantedBy=multi-user.target
`, p.User, p.Binary, p.Config)
}

func rcdScript(p serviceParams) string {
	return fmt.Sprintf(`#!/bin/sh
#
# PROVIDE: lotus
# REQUIRE: NETWORKING SERVERS
# KEYWORD: shutdown
#
# Add to /etc/rc.conf:   lotus_enable="YES"
# Optional:              lotus_config="%[3]s"   lotus_user="%[1]s"

. /etc/rc.subr

name=lotus
rcvar=lotus_enable
load_rc_config $name

: ${lotus_enable:=NO}
: ${lotus_user:=%[1]s}
: ${lotus_config:=%[3]s}

pidfile=/var/run/lotus.pid
command=/usr/sbin/daemon
command_args="-f -P ${pidfile} -u ${lotus_user} -r %[2]s start -config ${lotus_config}"
extra_commands=reload
reload_cmd="%[2]s reload"
stop_cmd="%[2]s stop"

run_rc_command "$1"
`, p.User, p.Binary, p.Config)
}
