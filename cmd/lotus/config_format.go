package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/laikait/lotus/internal/config/format"
)

// exitNotFormatted is returned by `lotus config format --check` when a file
// would change.
const exitNotFormatted = 9

const formatUsage = "usage: lotus config format [--check] [--write] <file>...   (a file named - is read from standard input)"

// cmdConfigFormat prints configuration files in the canonical style, or
// rewrites them (--write), or lists the ones that are not formatted (--check).
func cmdConfigFormat(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lotus config format", flag.ContinueOnError)
	fs.SetOutput(stderr)
	check := fs.Bool("check", false, "list the files that are not formatted, and change nothing (exit 9 if there are any)")
	write := fs.Bool("write", false, "rewrite the files in place")
	fs.BoolVar(write, "w", false, "short for --write")
	fs.Usage = func() {
		fmt.Fprintln(stderr, formatUsage)
		fs.PrintDefaults()
	}
	var files []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return exitUsage
		}
		if fs.NArg() == 0 {
			break
		}
		files = append(files, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(files) == 0 || (*check && *write) {
		fs.Usage()
		return exitUsage
	}
	if *write {
		for _, f := range files {
			if f == "-" {
				fmt.Fprintln(stderr, "lotus: standard input can't be rewritten in place")
				return exitUsage
			}
		}
	}

	code := exitOK
	for _, name := range files {
		var src []byte
		var err error
		if name == "-" {
			src, err = io.ReadAll(os.Stdin)
		} else {
			src, err = os.ReadFile(name)
		}
		if err != nil {
			fmt.Fprintf(stderr, "lotus: %v\n", err)
			return exitNotFound
		}
		out, diags, err := format.Source(name, src)
		if err != nil {
			for _, d := range diags {
				printDiagnostic(stderr, d)
			}
			fmt.Fprintf(stderr, "%s: not formatted: the file has syntax errors\n", name)
			code = exitInvalidConfig
			continue
		}
		changed := string(out) != string(src)
		switch {
		case *check:
			if changed {
				fmt.Fprintln(stdout, name)
				if code == exitOK {
					code = exitNotFormatted
				}
			}
		case *write:
			if changed {
				info, err := os.Stat(name)
				mode := os.FileMode(0o644)
				if err == nil {
					mode = info.Mode().Perm()
				}
				if err := os.WriteFile(name, out, mode); err != nil {
					fmt.Fprintf(stderr, "lotus: %v\n", err)
					return exitRuntime
				}
				fmt.Fprintf(stdout, "formatted %s\n", name)
			}
		default:
			if len(files) > 1 {
				fmt.Fprintf(stdout, "# %s\n", name)
			}
			_, _ = stdout.Write(out)
		}
	}
	return code
}
