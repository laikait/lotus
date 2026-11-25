package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/laikait/lotus/internal/sitefile"
)

func cmdSite(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: lotus site <add|list|remove> [options]")
		return exitUsage
	}
	switch args[0] {
	case "add":
		return cmdSiteAdd(args[1:], stdout, stderr)
	case "list", "ls":
		return cmdSiteList(args[1:], stdout, stderr)
	case "remove", "rm":
		return cmdSiteRemove(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "lotus site: unknown subcommand %q\n\nsubcommands: add, list, remove\n", args[0])
		return exitUsage
	}
}

// reportSiteError prints why a site change failed and returns the exit code.
func reportSiteError(stderr io.Writer, err error) int {
	var invalid *sitefile.InvalidError
	if errors.As(err, &invalid) {
		for _, d := range invalid.Diagnostics {
			printDiagnostic(stderr, d)
		}
		fmt.Fprintln(stderr, "lotus: the configuration is not valid with this change; nothing was changed")
		return exitInvalidConfig
	}
	fmt.Fprintf(stderr, "lotus: %v\n", err)
	return exitUsage
}

func cmdSiteAdd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lotus site add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg := fs.String("config", defaultConfigPath(), "main configuration file")
	root := fs.String("root", "", "web directory (default "+sitefile.DefaultRoot("NAME")+")")
	aliases := fs.String("aliases", "", "other host names, separated by commas (for example www.example.com)")
	index := fs.String("index", "index.html", "index file(s), separated by commas (for example index.php,index.html)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: lotus site add [--root DIR] [--aliases A,B] [--index FILES] [--config FILE] <name>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(reorderFlags(args)); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return exitUsage
	}
	store := &sitefile.Store{Config: *cfg}
	if hint := store.IncludeHint(); hint != "" {
		fmt.Fprintln(stderr, "note: "+hint)
	}
	sp, err := store.Add(sitefile.Spec{Name: fs.Arg(0), Aliases: splitList(*aliases), Root: *root, Index: splitList(*index)}, true)
	if err != nil {
		return reportSiteError(stderr, err)
	}
	fmt.Fprintf(stdout, "site %s added\n  settings: %s\n  web directory: %s\nApply it with: lotus reload (Administrator shell) or restart the LOTUS service.\n", sp.Name, store.File(sp.Name), sp.Root)
	return exitOK
}

func cmdSiteList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lotus site list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg := fs.String("config", defaultConfigPath(), "main configuration file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	store := &sitefile.Store{Config: *cfg}
	list, err := store.List()
	if err != nil {
		return reportSiteError(stderr, err)
	}
	if len(list) == 0 {
		fmt.Fprintf(stdout, "no sites in %s (add one with: lotus site add <name>)\n", store.Dir())
		return exitOK
	}
	for _, e := range list {
		note := ""
		if !e.Editable {
			note = "\t(changed by hand)"
		}
		fmt.Fprintln(stdout, e.Name+note)
	}
	return exitOK
}

func cmdSiteRemove(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lotus site remove", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg := fs.String("config", defaultConfigPath(), "main configuration file")
	if err := fs.Parse(reorderFlags(args)); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: lotus site remove [--config FILE] <name>")
		return exitUsage
	}
	name := strings.ToLower(fs.Arg(0))
	if err := (&sitefile.Store{Config: *cfg}).Remove(name); err != nil {
		if errors.Is(err, sitefile.ErrNotFound) {
			fmt.Fprintf(stderr, "lotus: no site %q here\n", name)
			return exitUsage
		}
		return reportSiteError(stderr, err)
	}
	fmt.Fprintf(stdout, "site %s removed (its web directory was not touched)\nApply it with: lotus reload (Administrator shell) or restart the LOTUS service.\n", name)
	return exitOK
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// reorderFlags moves flags before the positional name, so "site add example.com --root D" works as well as
// "site add --root D example.com" (Go's flag package stops at the first non-flag).
func reorderFlags(args []string) []string {
	var flags, rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && !isBoolFlag(a) && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				flags = append(flags, args[i])
			}
		} else {
			rest = append(rest, a)
		}
	}
	return append(flags, rest...)
}

// isBoolFlag names the flags of the commands that use reorderFlags that take no value.
func isBoolFlag(a string) bool {
	return strings.TrimLeft(a, "-") == "password-stdin"
}
