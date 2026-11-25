package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/laikait/lotus/internal/config"
	"github.com/laikait/lotus/internal/config/diag"
	"github.com/laikait/lotus/internal/config/loader"
)

// exitInvalidConfig is returned when a configuration has errors (docs/architecture/control-protocol.md §5).
const exitInvalidConfig = 2

func cmdConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: lotus config <validate|inspect|explain|format|migrate> [options] <file> [url]")
		return exitUsage
	}
	switch args[0] {
	case "validate":
		return cmdConfigValidate(args[1:], stdout, stderr)
	case "inspect":
		return cmdConfigInspect(args[1:], stdout, stderr)
	case "explain":
		return cmdConfigExplain(args[1:], stdout, stderr)
	case "format", "fmt":
		return cmdConfigFormat(args[1:], stdout, stderr)
	case "migrate":
		return cmdConfigMigrate(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "lotus config: unknown subcommand %q\n\nsubcommands: validate, inspect, explain\n", args[0])
		return exitUsage
	}
}

// cmdConfigInspect prints the effective configuration as JSON: defaults
// applied, inheritance merged, secrets redacted.
func cmdConfigInspect(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lotus config inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	site := fs.String("site", "", "show only this site")
	insecure := fs.Bool("insecure-includes", false, "skip permission checks on included files (development only)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: lotus config inspect [--site NAME] [--insecure-includes] <file>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return exitUsage
	}
	res, diags := config.Check(fs.Arg(0), loader.Options{InsecureIncludes: *insecure})
	if res == nil {
		for _, d := range diags {
			printDiagnostic(stderr, d)
		}
		fmt.Fprintf(stderr, "%s: configuration is invalid; nothing to inspect\n", fs.Arg(0))
		return exitInvalidConfig
	}
	out, err := res.InspectJSON(*site)
	if errors.Is(err, config.ErrNoSuchSite) {
		fmt.Fprintf(stderr, "lotus: there is no site named %q\n", *site)
		return exitUsage
	}
	if err != nil {
		fmt.Fprintf(stderr, "lotus: %v\n", err)
		return exitUsage
	}
	fmt.Fprintln(stdout, string(out))
	return exitOK
}

// validateReport is the --json output of `lotus config validate`.
type validateReport struct {
	Valid       bool              `json:"valid"`
	Version     int               `json:"config_version,omitempty"`
	Files       []string          `json:"files,omitempty"`
	Diagnostics []diag.Diagnostic `json:"diagnostics"`
}

func cmdConfigValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lotus config validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print the result as JSON on stdout")
	insecure := fs.Bool("insecure-includes", false, "skip permission checks on included files (development only)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: lotus config validate [--json] [--insecure-includes] <file>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return exitUsage
	}
	path := fs.Arg(0)

	res, diags := config.Check(path, loader.Options{InsecureIncludes: *insecure})
	if diags == nil {
		diags = []diag.Diagnostic{}
	}

	if *jsonOut {
		rep := validateReport{Valid: res != nil, Diagnostics: diags}
		if res != nil {
			rep.Version, rep.Files = res.Version, res.Files
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintf(stderr, "lotus: %v\n", err)
			return exitUsage
		}
	} else {
		for _, d := range diags {
			printDiagnostic(stderr, d)
		}
		if res != nil {
			fmt.Fprintf(stdout, "%s: configuration is valid (%d file(s))\n", path, len(res.Files))
		} else {
			errs := 0
			for _, d := range diags {
				if d.Severity == diag.Error {
					errs++
				}
			}
			fmt.Fprintf(stderr, "%s: configuration is invalid (%d error(s))\n", path, errs)
		}
	}
	if res == nil {
		return exitInvalidConfig
	}
	return exitOK
}

func printDiagnostic(w io.Writer, d diag.Diagnostic) {
	where := d.Pos.String()
	if !d.Pos.IsValid() {
		where = d.Pos.File
		if where == "" {
			where = "lotus"
		}
	}
	fmt.Fprintf(w, "%s: %s [%s]: %s\n", where, d.Severity, d.Code, d.Message)
	if d.Hint != "" {
		fmt.Fprintf(w, "    hint: %s\n", strings.TrimSpace(d.Hint))
	}
}
