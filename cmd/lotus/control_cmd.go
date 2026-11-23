package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/laikait/lotus/internal/config/diag"
	"github.com/laikait/lotus/internal/control"
	"github.com/laikait/lotus/pkg/version"
)

// Exit codes of the control commands (control-protocol.md §5).
const (
	exitUnreachable      = 3 // no running server answered on the control socket
	exitPermissionDenied = 4
	exitReloadBusy       = 5
	exitNotFound         = 7
)

// controlFlags adds --control to a flag set and returns the socket path to use.
func controlFlags(fs *flag.FlagSet) *string {
	def := os.Getenv("LOTUS_CONTROL")
	if def == "" {
		def = control.DefaultSocketPath()
	}
	return fs.String("control", def, "control socket of the running server (env LOTUS_CONTROL)")
}

// exitCodeFor maps a failed control call to the CLI exit code.
func exitCodeFor(err error) int {
	var ce *control.Error
	if errors.As(err, &ce) {
		switch ce.Code {
		case control.CodeParse, control.CodeInvalidRequest, control.CodeMethodNotFound, control.CodeInvalidParams:
			return exitUsage
		case control.CodePermissionDenied:
			return exitPermissionDenied
		case control.CodeUnsupportedProto:
			return exitUnreachable
		case control.CodeConfigInvalid:
			return exitInvalidConfig
		case control.CodeReloadInProgress:
			return exitReloadBusy
		case control.CodeNotSupported:
			return exitNotSupported
		case control.CodeNotFound:
			return exitNotFound
		}
		return exitRuntime
	}
	if errors.Is(err, control.ErrUnreachable) {
		return exitUnreachable
	}
	return exitRuntime
}

// dial connects to the running server, or prints why it can't.
func dial(socket string, stderr io.Writer) (*control.Client, int) {
	c, err := control.Dial(socket, "lotus-cli/"+version.Version)
	if err != nil {
		if errors.Is(err, control.ErrUnreachable) {
			fmt.Fprintf(stderr, "lotus: no running server answered on %s\n  is lotus running? use --control (or LOTUS_CONTROL) if its control socket is elsewhere\n", socket)
		} else {
			fmt.Fprintf(stderr, "lotus: %v\n", err)
		}
		return nil, exitCodeFor(err)
	}
	return c, exitOK
}

// parse parses flags for a control command that takes no arguments.
func parseNoArgs(fs *flag.FlagSet, args []string, stderr io.Writer, usage string) bool {
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: "+usage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return false
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return false
	}
	return true
}

func cmdReload(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lotus reload", flag.ContinueOnError)
	socket := controlFlags(fs)
	cfg := fs.String("config", "", "load this configuration file instead of the one the server uses now")
	if !parseNoArgs(fs, args, stderr, "lotus reload [--control SOCKET] [--config FILE]") {
		return exitUsage
	}
	c, code := dial(*socket, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	c.Timeout = 2 * time.Minute

	var res control.ReloadResult
	if err := c.Call(control.MethodReload, control.ReloadParams{Config: *cfg}, &res); err != nil {
		var ce *control.Error
		if errors.As(err, &ce) {
			for _, d := range diagnosticsOf(ce) {
				printDiagnostic(stderr, d)
			}
			fmt.Fprintf(stderr, "lotus: reload refused: %s\n", ce.Message)
		} else {
			fmt.Fprintf(stderr, "lotus: %v\n", err)
		}
		return exitCodeFor(err)
	}
	for _, raw := range res.Diagnostics {
		var d diag.Diagnostic
		if json.Unmarshal(raw, &d) == nil {
			printDiagnostic(stderr, d)
		}
	}
	fmt.Fprintf(stdout, "configuration reloaded (generation %d)\n", res.Generation)
	return exitOK
}

func cmdStop(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lotus stop", flag.ContinueOnError)
	socket := controlFlags(fs)
	now := fs.Bool("now", false, "don't wait for running requests")
	timeout := fs.Duration("timeout", 30*time.Second, "how long running requests may take to finish")
	if !parseNoArgs(fs, args, stderr, "lotus stop [--control SOCKET] [--now] [--timeout 30s]") {
		return exitUsage
	}
	c, code := dial(*socket, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	graceful := !*now
	if err := c.Call(control.MethodStop, control.StopParams{Graceful: &graceful, Timeout: timeout.String()}, nil); err != nil {
		fmt.Fprintf(stderr, "lotus: %v\n", err)
		return exitCodeFor(err)
	}
	fmt.Fprintln(stdout, "stopping")
	return exitOK
}

func cmdStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lotus status", flag.ContinueOnError)
	socket := controlFlags(fs)
	jsonOut := fs.Bool("json", false, "print the status as JSON")
	if !parseNoArgs(fs, args, stderr, "lotus status [--control SOCKET] [--json]") {
		return exitUsage
	}
	c, code := dial(*socket, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	var st control.Status
	if err := c.Call(control.MethodStatus, nil, &st); err != nil {
		fmt.Fprintf(stderr, "lotus: %v\n", err)
		return exitCodeFor(err)
	}
	if *jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(st)
		return exitOK
	}
	fmt.Fprintf(stdout, "lotus %s, pid %d, up %s\n", st.Version, st.PID, st.Uptime)
	fmt.Fprintf(stdout, "configuration: %s (generation %d)\n", st.ConfigFile, st.Generation)
	fmt.Fprintf(stdout, "sites: %d\n", st.Sites)
	for _, l := range st.Listeners {
		proto := "http"
		if l.HTTPS {
			proto = "https"
		}
		fmt.Fprintf(stdout, "listener %s: %s on %s\n", l.Name, proto, l.Address)
	}
	fmt.Fprintf(stdout, "goroutines: %d, memory in use: %.1f MiB\n", st.Goroutines, float64(st.MemoryBytes)/(1<<20))
	fmt.Fprintf(stdout, "connections refused at the limit: %d, requests rejected before routing: %d\n", st.Rejected, st.Dropped)
	return exitOK
}

func cmdLogs(args []string, stdout, stderr io.Writer) int {
	const usage = "lotus logs reopen [--control SOCKET]"
	// The subcommand may come before or after the flags.
	var rest []string
	found := false
	for i, a := range args {
		if a == "reopen" && !found {
			found = true
			rest = append(append([]string{}, args[:i]...), args[i+1:]...)
			break
		}
	}
	if !found {
		fmt.Fprintln(stderr, "usage: "+usage)
		return exitUsage
	}
	fs := flag.NewFlagSet("lotus logs reopen", flag.ContinueOnError)
	socket := controlFlags(fs)
	if !parseNoArgs(fs, rest, stderr, usage) {
		return exitUsage
	}
	c, code := dial(*socket, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	var res struct {
		Reopened int `json:"reopened"`
	}
	if err := c.Call(control.MethodLogsReopen, nil, &res); err != nil {
		fmt.Fprintf(stderr, "lotus: %v\n", err)
		return exitCodeFor(err)
	}
	fmt.Fprintf(stdout, "reopened %d log file(s)\n", res.Reopened)
	return exitOK
}
