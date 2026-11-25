package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/laikait/lotus/internal/config/migrate"
)

const migrateUsage = "usage: lotus config migrate [--check] [--write] <file>"

// cmdConfigMigrate brings a configuration file up to the language version this
// build understands. With one language version so far, it mostly says that a
// file is current; it is the place where the first incompatible change will
// ship its migration.
func cmdConfigMigrate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lotus config migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	check := fs.Bool("check", false, "report whether the file needs migrating, and change nothing (exit 9 if it does)")
	write := fs.Bool("write", false, "rewrite the file in place")
	fs.BoolVar(write, "w", false, "short for --write")
	fs.Usage = func() {
		fmt.Fprintln(stderr, migrateUsage)
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
	if len(files) != 1 || (*check && *write) {
		fs.Usage()
		return exitUsage
	}
	name := files[0]
	src, err := os.ReadFile(name)
	if err != nil {
		fmt.Fprintf(stderr, "lotus: %v\n", err)
		return exitNotFound
	}
	res, diags, err := migrate.Source(name, src)
	switch {
	case errors.Is(err, migrate.ErrSyntax):
		for _, d := range diags {
			printDiagnostic(stderr, d)
		}
		fmt.Fprintf(stderr, "%s: not migrated: the file has syntax errors\n", name)
		return exitInvalidConfig
	case err != nil:
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return exitInvalidConfig
	}
	if !res.Changed {
		fmt.Fprintf(stdout, "%s: language version %d is the current one; nothing to migrate\n", name, res.From)
		return exitOK
	}
	switch {
	case *check:
		fmt.Fprintf(stdout, "%s: language version %d; migrating to %d would apply %d step(s)\n", name, res.From, res.To, len(res.Applied))
		return exitNotFormatted
	case *write:
		info, serr := os.Stat(name)
		mode := os.FileMode(0o644)
		if serr == nil {
			mode = info.Mode().Perm()
		}
		if err := os.WriteFile(name, res.Output, mode); err != nil {
			fmt.Fprintf(stderr, "lotus: %v\n", err)
			return exitRuntime
		}
		fmt.Fprintf(stdout, "%s: migrated from language version %d to %d\n", name, res.From, res.To)
		for _, a := range res.Applied {
			fmt.Fprintf(stdout, "  %s\n", a)
		}
		return exitOK
	}
	_, _ = stdout.Write(res.Output)
	for _, a := range res.Applied {
		fmt.Fprintf(stderr, "applied: %s\n", a)
	}
	return exitOK
}
