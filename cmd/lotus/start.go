package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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

// Exit codes for start (docs/architecture/control-protocol.md §5).
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
// SIGTERM. Service managers (systemd, the Windows service control manager,
// rc.d) are responsible for running it in the background.
func cmdStart(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runStart(ctx, args, stdout, stderr, nil)
}

// runStart is cmdStart with an explicit context, so tests can stop the server.
// ready, if not nil, is called once the listeners are bound.
func runStart(ctx context.Context, args []string, stdout, stderr io.Writer, ready func(map[string]net.Addr)) int {
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

	if cfg.Runtime.MaxProcs > 0 {
		runtime.GOMAXPROCS(cfg.Runtime.MaxProcs)
	}
	if cfg.Runtime.MemoryLimit > 0 {
		debug.SetMemoryLimit(cfg.Runtime.MemoryLimit)
	}

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
	if err := srv.Start(); err != nil {
		fmt.Fprintf(stderr, "lotus: cannot start: %v\n", err)
		return exitRuntime
	}
	log.Info("started", "config", path, "sites", len(cfg.Sites), "listeners", len(cfg.Listeners))
	if ready != nil {
		ready(srv.Addrs())
	}

	code := exitOK
	select {
	case <-ctx.Done():
		log.Info("shutting down", "timeout", shutdownTimeout.String())
	case err := <-srv.Errors():
		log.Error("a listener stopped unexpectedly", "error", err)
		code = exitRuntime
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown did not finish cleanly", "error", err)
		if code == exitOK {
			code = exitRuntime
		}
	}
	log.Info("stopped")
	return code
}
