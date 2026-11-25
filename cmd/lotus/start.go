package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/laikait/lotus/internal/config"
	"github.com/laikait/lotus/internal/config/loader"
	"github.com/laikait/lotus/internal/logging"
	"github.com/laikait/lotus/internal/server"
)

// Exit codes (docs/architecture/control-protocol.md §5).
const (
	exitNotSupported = 6  // the configuration uses a feature that isn't implemented yet
	exitRuntime      = 10 // the server could not start, or stopped because of an error
)

// shutdownTimeout is how long running requests get to finish on shutdown.
const shutdownTimeout = 30 * time.Second

// defaultConfigPath is where lotus looks for its configuration when -config is not given.
func defaultConfigPath() string {
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		return filepath.Join(base, "LOTUS", "main.lotus")
	case "freebsd", "openbsd", "netbsd", "dragonfly":
		return "/usr/local/etc/lotus/main.lotus"
	}
	return "/etc/lotus/main.lotus"
}

// cmdStart runs the server in the foreground until it receives SIGINT or
// SIGTERM, and reloads its configuration on SIGHUP (Unix). `lotus reload` and
// `lotus stop` do the same through the control channel, which also works on
// Windows. Service managers (systemd, the Windows service control manager,
// rc.d) are responsible for running it in the background.
func cmdStart(args []string, stdout, stderr io.Writer) int {
	if code, ok := runAsService(args, stdout, stderr); ok {
		return code
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// SIGHUP is never delivered on Windows.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	reloads := make(chan struct{}, 1)
	go func() {
		for range hup {
			select {
			case reloads <- struct{}{}:
			default: // a reload is already waiting; one more would do the same
			}
		}
	}()
	return runServer(ctx, args, stdout, stderr, nil, reloads)
}

// runStart is runServer without reloads.
func runStart(ctx context.Context, args []string, stdout, stderr io.Writer, ready func(map[string]net.Addr)) int {
	return runServer(ctx, args, stdout, stderr, ready, nil)
}

// runServer starts the server and runs it until ctx ends or a stop is
// requested. Each value received on reloads makes it re-read the configuration
// file and switch to it; ready, if not nil, is called once the listeners are
// bound.
func runServer(ctx context.Context, args []string, stdout, stderr io.Writer, ready func(map[string]net.Addr), reloads <-chan struct{}) int {
	fs := flag.NewFlagSet("lotus start", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "configuration file (default "+defaultConfigPath()+")")
	fs.StringVar(cfgPath, "c", "", "shorthand for -config")
	insecure := fs.Bool("insecure-includes", false, "skip permission checks on included files (development only)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: lotus start [-config FILE] [--insecure-includes]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return exitUsage
	}
	path := *cfgPath
	if path == "" {
		path = defaultConfigPath()
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}

	res, diags := config.Check(path, loader.Options{InsecureIncludes: *insecure})
	if res == nil {
		for _, d := range diags {
			printDiagnostic(stderr, d)
		}
		fmt.Fprintf(stderr, "%s: configuration is invalid; not starting\n", path)
		return exitInvalidConfig
	}
	for _, d := range diags { // warnings
		printDiagnostic(stderr, d)
	}
	cfg := res.Config

	rt := newRuntimeSettings()
	rt.apply(cfg.Runtime.MaxProcs, cfg.Runtime.MemoryLimit)

	log := logging.NewErrorLogger(stderr, "server")
	srv, err := server.New(cfg, server.Options{Logger: log, Stdout: stdout, Stderr: stderr})
	if err != nil {
		var unsupported *server.UnsupportedError
		if errors.As(err, &unsupported) {
			fmt.Fprintf(stderr, "lotus: %v\n", err)
			return exitNotSupported
		}
		fmt.Fprintf(stderr, "lotus: %v\n", err)
		return exitRuntime
	}

	a := &app{
		path: path, insecure: *insecure, srv: srv, rt: rt, log: log, stderr: stderr,
		started: time.Now(), res: res, stop: make(chan stopRequest, 1),
	}

	// The control socket is opened before the listeners so that a bad explicit
	// path fails the start instead of leaving a half-working server.
	ctl, err := a.openControl(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "lotus: control channel: %v\n", err)
		_ = srv.Shutdown(context.Background())
		return exitRuntime
	}
	closeControl := func() {
		if ctl != nil {
			ctl.Close()
		}
	}

	if err := srv.Start(); err != nil {
		closeControl()
		fmt.Fprintf(stderr, "lotus: cannot start: %v\n", err)
		return exitRuntime
	}
	log.Info("started", "config", path, "sites", len(cfg.Sites), "listeners", len(cfg.Listeners))
	if ready != nil {
		ready(srv.Addrs())
	}

	code := exitOK
	timeout := shutdownTimeout
	graceful := true
wait:
	for {
		select {
		case <-ctx.Done():
			log.Info("shutting down", "timeout", shutdownTimeout.String())
			break wait
		case req := <-a.stop:
			graceful, timeout = req.graceful, req.timeout
			log.Info("stop requested through the control channel", "graceful", graceful, "timeout", timeout.String())
			break wait
		case err := <-srv.Errors():
			log.Error("a listener stopped unexpectedly", "error", err)
			code = exitRuntime
			break wait
		case <-reloads:
			if _, rerr := a.reload(""); rerr != nil {
				log.Error("reload did not complete", "reason", rerr.Message)
			}
		}
	}

	closeControl() // no new commands while shutting down
	if !graceful {
		timeout = time.Millisecond // close the connections that are left almost at once
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && graceful {
		log.Error("shutdown did not finish cleanly", "error", err)
		if code == exitOK {
			code = exitRuntime
		}
	}
	log.Info("stopped")
	return code
}

// runtimeSettings applies the runtime block of the configuration, and can
// return to Go's defaults when a setting is removed.
type runtimeSettings struct {
	defaultProcs int
}

func newRuntimeSettings() *runtimeSettings {
	return &runtimeSettings{defaultProcs: runtime.GOMAXPROCS(0)}
}

// apply sets GOMAXPROCS and the soft memory limit; 0 means "the default".
func (r *runtimeSettings) apply(maxProcs int, memoryLimit int64) {
	if maxProcs <= 0 {
		maxProcs = r.defaultProcs
	}
	if maxProcs != runtime.GOMAXPROCS(0) {
		runtime.GOMAXPROCS(maxProcs)
	}
	if memoryLimit <= 0 {
		memoryLimit = math.MaxInt64
	}
	if memoryLimit != debug.SetMemoryLimit(-1) {
		debug.SetMemoryLimit(memoryLimit)
	}
}
