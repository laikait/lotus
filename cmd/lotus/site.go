package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/laikait/lotus/internal/config"
	"github.com/laikait/lotus/internal/config/loader"
)

// siteIncludeLine is what the main configuration needs, inside its server block,
// for the files made by "lotus site add" to be read.
const siteIncludeLine = `include "sites.d/*.lotus"`

var siteNameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$`)

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

// defaultSiteRoot is the web directory a new site gets when --root is not given.
func defaultSiteRoot(name string) string {
	if runtime.GOOS == "windows" {
		return "C:/www/" + name
	}
	return "/var/www/public_html/" + name
}

func sitesDir(cfg string) string { return filepath.Join(filepath.Dir(cfg), "sites.d") }

// siteFile is where the site "name" is kept; the name is checked before it is used.
func siteFile(cfg, name string) string { return filepath.Join(sitesDir(cfg), name+".lotus") }

func cmdSiteAdd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lotus site add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg := fs.String("config", defaultConfigPath(), "main configuration file")
	root := fs.String("root", "", "web directory (default "+defaultSiteRoot("NAME")+")")
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
	name := strings.ToLower(fs.Arg(0))
	if !siteNameRe.MatchString(name) {
		fmt.Fprintf(stderr, "lotus: %q is not a host name (letters, digits, dots and dashes)\n", fs.Arg(0))
		return exitUsage
	}
	dir := *root
	if dir == "" {
		dir = defaultSiteRoot(name)
	}
	dir = filepath.ToSlash(dir)

	if hint := siteIncludeHint(*cfg); hint != "" {
		fmt.Fprintln(stderr, hint)
	}
	file := siteFile(*cfg, name)
	if _, err := os.Stat(file); err == nil {
		fmt.Fprintf(stderr, "lotus: site %q already exists (%s)\n", name, file)
		return exitUsage
	}
	if err := os.MkdirAll(sitesDir(*cfg), 0o755); err != nil {
		fmt.Fprintf(stderr, "lotus: %v\n", err)
		return exitUsage
	}
	if err := os.WriteFile(file, []byte(renderSite(name, dir, splitList(*aliases), splitList(*index))), 0o644); err != nil {
		fmt.Fprintf(stderr, "lotus: %v\n", err)
		return exitUsage
	}

	// The site is kept only if the whole configuration is still valid with it.
	res, diags := config.Check(*cfg, loader.Options{})
	if res == nil {
		_ = os.Remove(file)
		for _, d := range diags {
			printDiagnostic(stderr, d)
		}
		fmt.Fprintf(stderr, "lotus: the configuration is not valid with this site; it was not added\n")
		return exitInvalidConfig
	}

	made := ""
	if err := os.MkdirAll(dir, 0o755); err == nil {
		page := filepath.Join(filepath.FromSlash(dir), "index.html")
		if _, err := os.Stat(page); err != nil && containsString(splitList(*index), "index.html") {
			body := "<!doctype html>\n<meta charset=\"utf-8\">\n<title>" + name + "</title>\n<h1>" + name + "</h1>\n<p>This site is served by LOTUS.</p>\n"
			if os.WriteFile(page, []byte(body), 0o644) == nil {
				made = " with a sample page"
			}
		}
	}
	fmt.Fprintf(stdout, "site %s added%s\n  settings: %s\n  web directory: %s\nApply it with: lotus reload (Administrator shell) or restart the LOTUS service.\n", name, made, file, dir)
	return exitOK
}

func cmdSiteList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lotus site list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg := fs.String("config", defaultConfigPath(), "main configuration file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	files, _ := filepath.Glob(filepath.Join(sitesDir(*cfg), "*.lotus"))
	sort.Strings(files)
	if len(files) == 0 {
		fmt.Fprintf(stdout, "no sites in %s (add one with: lotus site add <name>)\n", sitesDir(*cfg))
		return exitOK
	}
	for _, f := range files {
		fmt.Fprintln(stdout, strings.TrimSuffix(filepath.Base(f), ".lotus"))
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
	if !siteNameRe.MatchString(name) {
		fmt.Fprintf(stderr, "lotus: %q is not a host name\n", fs.Arg(0))
		return exitUsage
	}
	file := siteFile(*cfg, name)
	if err := os.Remove(file); err != nil {
		fmt.Fprintf(stderr, "lotus: no site %q here (%s)\n", name, file)
		return exitUsage
	}
	fmt.Fprintf(stdout, "site %s removed (its web directory was not touched)\nApply it with: lotus reload (Administrator shell) or restart the LOTUS service.\n", name)
	return exitOK
}

// renderSite is the text of one site file.
func renderSite(name, root string, aliases, index []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Made by \"lotus site add\". Edit it freely; \"lotus site remove %s\" deletes this file.\n", name)
	fmt.Fprintf(&b, "site %q {\n", name)
	if len(aliases) > 0 {
		fmt.Fprintf(&b, "    aliases %s\n", quoteList(aliases))
	}
	fmt.Fprintf(&b, "    root %q\n", root)
	fmt.Fprintf(&b, "    index %s\n", quoteList(index))
	b.WriteString("    tls off\n}\n")
	return b.String()
}

func quoteList(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = fmt.Sprintf("%q", s)
	}
	return "[ " + strings.Join(q, ", ") + " ]"
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

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// siteIncludeHint says what to add to a main configuration that does not read
// sites.d yet (an installation from before "lotus site" existed).
func siteIncludeHint(cfg string) string {
	data, err := os.ReadFile(cfg)
	if err != nil || strings.Contains(string(data), "sites.d") {
		return ""
	}
	return "note: " + cfg + " does not read the sites.d folder yet. Add this line inside its server { } block:\n    " + siteIncludeLine
}

// reorderFlags moves flags before the positional name, so "site add example.com --root D" works as well as
// "site add --root D example.com" (Go's flag package stops at the first non-flag).
func reorderFlags(args []string) []string {
	var flags, rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				flags = append(flags, args[i])
			}
		} else {
			rest = append(rest, a)
		}
	}
	return append(flags, rest...)
}
