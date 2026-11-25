// Command lotus is the LOTUS (Laika Optimized Transport & Unified Server) executable.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/laikait/lotus/internal/control"
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
	case "reload":
		return cmdReload(args[1:], stdout, stderr)
	case "stop":
		return cmdStop(args[1:], stdout, stderr)
	case "status":
		return cmdStatus(args[1:], stdout, stderr)
	case "certs":
		return cmdCerts(args[1:], stdout, stderr)
	case "cache":
		return cmdCache(args[1:], stdout, stderr)
	case "logs":
		return cmdLogs(args[1:], stdout, stderr)
	case "compat":
		return cmdCompat(args[1:], stdout, stderr)
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
	fmt.Fprintf(w, `LOTUS — Laika Optimized Transport & Unified Server
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
  config explain [options] <file> <url>
                              Show which site, route and action a request would get
  reload [--control SOCKET] [--config FILE]
                              Make the running server load its configuration again
  stop [--now] [--timeout 30s]
                              Stop the running server, letting requests finish
  status [--json]             Show the running server's state
  certs [--json] [--warn-days N]
                              List the certificates in use and when they expire
  cache status [--zone NAME] [--json]
                              Show the response cache zones: size, entries, hit rate
  cache purge [--zone NAME] [--host HOST] (--path PATH | --prefix PREFIX | --all)
                              Remove stored responses from a cache zone
  logs reopen                 Reopen the log files (after log rotation)
  compat htaccess check [--json] [--all] <file>
                              Report which directives of a .htaccess file LOTUS can apply
  compat nginx convert [--json] [--all] [-o file [--force]] <nginx.conf>
                              Translate an Nginx configuration to LOTUS and list what needs review
  help                        Show this help

The reload, stop, status, certs, cache and logs commands talk to the running server through its control
socket (default %s; set another with --control or LOTUS_CONTROL).

Exit codes: 0 success, 1 usage error, 2 invalid configuration, 3 no running server,
4 permission denied, 5 reload already running, 6 uses a feature not implemented yet,
7 not found, 8 a certificate is missing, expired or about to expire (lotus certs),
10 the server could not start or failed.
`, control.DefaultSocketPath())
}
