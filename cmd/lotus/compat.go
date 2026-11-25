package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/laikait/lotus/internal/htaccess"
)

// cmdCompat is `lotus compat ...`: tools for moving from other servers.
func cmdCompat(args []string, stdout, stderr io.Writer) int {
	if len(args) >= 2 && args[0] == "htaccess" && args[1] == "check" {
		return cmdCompatHtaccessCheck(args[2:], stdout, stderr)
	}
	if len(args) >= 1 && args[0] == "nginx" {
		return cmdCompatNginx(args[1:], stdout, stderr)
	}
	fmt.Fprintln(stderr, "usage: lotus compat htaccess check [--json] [--all] [--allow categories] <file>")
	fmt.Fprintln(stderr, "       "+compatNginxUsage[len("usage: "):])
	fmt.Fprintln(stderr, "(importing Apache httpd configurations is not implemented yet)")
	return exitUsage
}

// cmdCompatHtaccessCheck reports what LOTUS would do with each directive of a
// .htaccess file, so an administrator knows before enabling it for a site.
func cmdCompatHtaccessCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lotus compat htaccess check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print the result as JSON")
	all := fs.Bool("all", false, "list every directive, not only those that are not fully supported")
	allow := fs.String("allow", strings.Join(htaccess.Categories, ","), "directive categories the site allows (htaccess_allow), comma separated")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: lotus compat htaccess check [--json] [--all] [--allow categories] <file>")
		fs.PrintDefaults()
	}
	// Flags may come before or after the file name.
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
	if len(files) != 1 {
		fs.Usage()
		return exitUsage
	}

	allowed := map[string]bool{}
	for _, c := range strings.Split(*allow, ",") {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		known := false
		for _, k := range htaccess.Categories {
			known = known || k == c
		}
		if !known {
			fmt.Fprintf(stderr, "lotus: unknown category %q (known: %s)\n", c, strings.Join(htaccess.Categories, ", "))
			return exitUsage
		}
		allowed[c] = true
	}

	f, err := os.Open(files[0])
	if err != nil {
		fmt.Fprintf(stderr, "lotus: %v\n", err)
		return exitNotFound
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, htaccess.MaxFileSize+1))
	if err != nil {
		fmt.Fprintf(stderr, "lotus: %v\n", err)
		return exitRuntime
	}
	nodes, problems := htaccess.Parse(data)
	c := htaccess.Compile(nodes, problems, htaccess.Options{Allow: allowed})

	counts := map[htaccess.Status]int{}
	for _, l := range c.Lines {
		counts[l.Status]++
	}
	bad := counts[htaccess.Unsupported] + counts[htaccess.Disallowed]

	if *jsonOut {
		type jsonLine struct {
			Line      int    `json:"line"`
			Directive string `json:"directive"`
			Status    string `json:"status"`
			Message   string `json:"message,omitempty"`
			Sensitive bool   `json:"sensitive,omitempty"`
		}
		out := struct {
			File    string     `json:"file"`
			Lines   []jsonLine `json:"directives"`
			Notes   []string   `json:"notes,omitempty"`
			Problem int        `json:"problems"`
		}{File: files[0], Problem: bad, Lines: []jsonLine{}}
		for _, l := range c.Lines {
			out.Lines = append(out.Lines, jsonLine{l.Line, l.Directive, l.Status.String(), l.Msg, l.Sensitive})
		}
		for _, n := range c.Notes {
			out.Notes = append(out.Notes, fmt.Sprintf("line %d: %s", n.Line, n.Msg))
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
	} else {
		fmt.Fprintf(stdout, "%s: %d directive(s): %d supported, %d ignored, %d not supported or not allowed\n",
			files[0], len(c.Lines), counts[htaccess.Supported], counts[htaccess.Ignored], bad)
		for _, l := range c.Lines {
			if l.Status == htaccess.Supported && !*all {
				continue
			}
			label := strings.ToUpper(l.Status.String())
			if l.Status == htaccess.Supported {
				label = "supported"
			}
			note := ""
			if l.Sensitive && (l.Status == htaccess.Unsupported || l.Status == htaccess.Disallowed) {
				note = " [access control: never skipped]"
			}
			if l.Msg != "" {
				fmt.Fprintf(stdout, "  line %-4d %-20s %s: %s%s\n", l.Line, l.Directive, label, l.Msg, note)
			} else {
				fmt.Fprintf(stdout, "  line %-4d %-20s %s\n", l.Line, l.Directive, label)
			}
		}
		for _, n := range c.Notes {
			fmt.Fprintf(stdout, "  line %-4d note: %s\n", n.Line, n.Msg)
		}
		if bad == 0 {
			fmt.Fprintln(stdout, "result: LOTUS can apply this file as it is")
		} else {
			fmt.Fprintf(stdout, "result: %d directive(s) LOTUS can't apply; a site with htaccess_unsupported error answers 500 for this directory\n", bad)
		}
	}
	if bad > 0 {
		return exitNotSupported
	}
	return exitOK
}
