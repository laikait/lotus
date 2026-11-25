//go:build windows

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// serviceName is the name of the Windows service.
const serviceName = "LOTUS"

func serviceInstall(p serviceParams, force bool, stdout, stderr io.Writer) int {
	m, err := mgr.Connect()
	if err != nil {
		fmt.Fprintf(stderr, "lotus: cannot reach the service manager: %v (run as an administrator)\n", err)
		return exitRuntime
	}
	defer m.Disconnect()
	if s, err := m.OpenService(serviceName); err == nil {
		s.Close()
		if !force {
			fmt.Fprintf(stderr, "lotus: the %s service exists; use --force to replace it\n", serviceName)
			return exitUsage
		}
		if code := serviceUninstall(io.Discard, stderr); code != exitOK {
			return code
		}
	}
	s, err := m.CreateService(serviceName, p.Binary, mgr.Config{
		DisplayName: "LOTUS web server",
		Description: "LOTUS web server (https://github.com/laikait/lotus)",
		StartType:   mgr.StartAutomatic,
	}, "start", "-config", p.Config)
	if err != nil {
		fmt.Fprintf(stderr, "lotus: %v\n", err)
		return exitRuntime
	}
	defer s.Close()
	// Restart after a crash: twice quickly, then every minute.
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 2 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: time.Minute},
	}, 86400)
	fmt.Fprintf(stdout, "installed the %s service (configuration %s)\nstart it with:  net start %s   (or: Start-Service %s)\nlogs go to %s\n",
		serviceName, p.Config, serviceName, serviceName, serviceLogPath())
	return exitOK
}

func serviceUninstall(stdout, stderr io.Writer) int {
	m, err := mgr.Connect()
	if err != nil {
		fmt.Fprintf(stderr, "lotus: cannot reach the service manager: %v (run as an administrator)\n", err)
		return exitRuntime
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		fmt.Fprintf(stderr, "lotus: the %s service is not installed\n", serviceName)
		return exitNotFound
	}
	defer s.Close()
	if st, err := s.Query(); err == nil && st.State != svc.Stopped {
		_, _ = s.Control(svc.Stop)
		for i := 0; i < 50; i++ {
			if st, err := s.Query(); err != nil || st.State == svc.Stopped {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	if err := s.Delete(); err != nil {
		fmt.Fprintf(stderr, "lotus: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(stdout, "removed the %s service\n", serviceName)
	return exitOK
}

func serviceLogPath() string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "LOTUS", "logs", "lotus.log")
}

// runAsService runs the server under the Windows service manager when that is
// what started this process. The second result is false for a normal start.
func runAsService(args []string, stdout, stderr io.Writer) (int, bool) {
	isSvc, err := svc.IsWindowsService()
	if err != nil || !isSvc {
		return 0, false
	}
	// A service has no console: everything the server prints goes to a file.
	var out io.Writer = io.Discard
	if err := os.MkdirAll(filepath.Dir(serviceLogPath()), 0o755); err == nil {
		if f, err := os.OpenFile(serviceLogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640); err == nil {
			defer f.Close()
			out = f
		}
	}
	h := &serviceHandler{args: args, out: out}
	if err := svc.Run(serviceName, h); err != nil {
		fmt.Fprintf(out, "lotus: service: %v\n", err)
		return exitRuntime, true
	}
	return h.code, true
}

type serviceHandler struct {
	args []string
	out  io.Writer
	code int
}

func (h *serviceHandler) Execute(_ []string, r <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	ready := make(chan struct{})
	go func() {
		done <- runStart(ctx, h.args, h.out, h.out, func(map[string]net.Addr) { close(ready) })
	}()
	status <- svc.Status{State: svc.StartPending}
	select {
	case <-ready:
	case h.code = <-done: // it could not start
		return false, 1
	}
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending, WaitHint: uint32(shutdownTimeout/time.Millisecond) + 5000}
				cancel()
				h.code = <-done
				return false, 0
			}
		case h.code = <-done: // stopped by `lotus stop` or a failure
			if h.code != exitOK {
				return false, uint32(h.code)
			}
			return false, 0
		}
	}
}
