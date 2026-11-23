// Command lotus is the LOTUS (Laika Optimized Transport & Unified Server) executable.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/laikait/lotus/pkg/version"
)

// Exit codes (spec §8, control protocol §5).
const (
	exitOK    = 0
	exitUsage = 1
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printHelp(stderr)
		return exitUsage
	}

	switch args[0] {
	case "version", "--version", "-v":
		return cmdVersion(args[1:], stdout, stderr)
	case "config":
		return cmdConfig(args[1:], stdout, stderr)
	case "start":
		return cmdStart(args[1:], stdout, stderr)
	case "help", "--help", "-h":
		printHelp(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "lotus: unknown command %q\n\n", args[0])
		printHelp(stderr)
		return exitUsage
	}
}

func cmdVersion(args []string, stdout, stderr io.Writer) int {
	info := version.Get()
	switch {
	case len(args) == 0:
		fmt.Fprintln(stdout, info.String())
	case len(args) == 1 && args[0] == "--json":
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(info); err != nil {
			fmt.Fprintf(stderr, "lotus: %v\n", err)
			return exitUsage
		}
	default:
		fmt.Fprintln(stderr, "usage: lotus version [--json]")
		return exitUsage
	}
	return exitOK
}

func printHelp(w io.Writer) {
	fmt.Fprint(w, `LOTUS — Laika Optimized Transport & Unified Server
A next-generation, cross-platform web server.

Usage:
  lotus <command> [options]

Commands:
  version [--json]            Print build information
  start [-config FILE]        Run the server in the foreground (stop with Ctrl+C or SIGTERM)
  config validate [--json] <file>
                              Check a configuration file and everything it includes
  config inspect [--site NAME] <file>
                              Print the effective configuration as JSON (secrets redacted)
  help                        Show this help

Exit codes: 0 success, 1 usage error, 2 invalid configuration, 6 uses a feature not
implemented yet, 10 the server could not start or failed.

More commands (start, reload, config format, ...) arrive in Phase 1.
`)
}
