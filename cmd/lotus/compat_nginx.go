package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/laikait/lotus/internal/config"
	"github.com/laikait/lotus/internal/config/diag"
	"github.com/laikait/lotus/internal/config/loader"
	"github.com/laikait/lotus/internal/nginxconv"
)

const compatNginxUsage = "usage: lotus compat nginx convert [--json] [--all] [--output file [--force]] <nginx.conf>"

// cmdCompatNginx is `lotus compat nginx ...`.
func cmdCompatNginx(args []string, stdout, stderr io.Writer) int {
	if len(args) >= 1 && args[0] == "convert" {
		return cmdCompatNginxConvert(args[1:], stdout, stderr)
	}
	fmt.Fprintln(stderr, compatNginxUsage)
	return exitUsage
}

// cmdCompatNginxConvert translates an Nginx configuration into a LOTUS one and
// reports what could not be translated exactly.
func cmdCompatNginxConvert(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lotus compat nginx convert", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print the result as JSON")
	all := fs.Bool("all", false, "list every directive, including the ones translated exactly and the ones ignored")
	output := fs.String("output", "", "write the LOTUS configuration to this file instead of standard output")
	force := fs.Bool("force", false, "overwrite the output file if it exists")
	fs.StringVar(output, "o", "", "short for --output")
	fs.Usage = func() {
		fmt.Fprintln(stderr, compatNginxUsage)
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
	if len(files) != 1 {
		fs.Usage()
		return exitUsage
	}
	src := files[0]
	if _, err := os.Stat(src); err != nil {
		fmt.Fprintf(stderr, "lotus: %v\n", err)
		return exitNotFound
	}
	if *output != "" && !*force {
		if _, err := os.Stat(*output); err == nil {
			fmt.Fprintf(stderr, "lotus: %s exists; use --force to overwrite it\n", *output)
			return exitUsage
		}
	}

	nodes, err := nginxconv.Parse(src, nginxconv.Options{})
	if err != nil {
		fmt.Fprintf(stderr, "lotus: %s: %v\n", src, err)
		return exitInvalidConfig
	}
	res := nginxconv.Convert(nodes, *all)

	// The translation must be something LOTUS accepts. If it is not, that is a
	// bug in the translator; say so and still show what was produced.
	valid, diags := checkText(res.Config)

	base := filepath.Dir(src)
	rel := func(p string) string {
		if r, err := filepath.Rel(base, p); err == nil && !strings.HasPrefix(r, "..") {
			return filepath.ToSlash(r)
		}
		return filepath.ToSlash(p)
	}
	counts := map[nginxconv.Status]int{}
	for _, i := range res.Issues {
		counts[i.Status]++
	}

	if *output != "" {
		if err := os.WriteFile(*output, []byte(res.Config), 0o644); err != nil {
			fmt.Fprintf(stderr, "lotus: %v\n", err)
			return exitRuntime
		}
	}

	if *jsonOut {
		type jsonIssue struct {
			File      string `json:"file"`
			Line      int    `json:"line"`
			Directive string `json:"directive"`
			Status    string `json:"status"`
			Message   string `json:"message,omitempty"`
		}
		rep := struct {
			Source       string      `json:"source"`
			Config       string      `json:"config,omitempty"`
			Valid        bool        `json:"valid"`
			Unsupported  int         `json:"unsupported"`
			Approximated int         `json:"approximated"`
			Ignored      int         `json:"ignored"`
			Issues       []jsonIssue `json:"issues"`
		}{Source: src, Valid: valid, Unsupported: counts[nginxconv.Unsupported], Approximated: counts[nginxconv.Approximated],
			Ignored: counts[nginxconv.Ignored], Issues: []jsonIssue{}}
		if *output == "" {
			rep.Config = res.Config
		}
		for _, i := range res.Issues {
			rep.Issues = append(rep.Issues, jsonIssue{rel(i.File), i.Line, i.Directive, string(i.Status), i.Message})
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		rep := stderr
		if *output != "" {
			rep = stdout
		} else {
			fmt.Fprint(stdout, res.Config)
		}
		fmt.Fprintf(rep, "%s: %d unsupported, %d approximated, %d ignored\n", src,
			counts[nginxconv.Unsupported], counts[nginxconv.Approximated], counts[nginxconv.Ignored])
		for _, i := range res.Issues {
			if !*all && (i.Status == nginxconv.Ignored || i.Status == nginxconv.Translated) {
				continue
			}
			label := strings.ToUpper(string(i.Status))
			if i.Status == nginxconv.Translated {
				label = "translated"
			}
			msg := ""
			if i.Message != "" {
				msg = ": " + i.Message
			}
			fmt.Fprintf(rep, "  %s:%-4d %-22s %s%s\n", rel(i.File), i.Line, i.Directive, label, msg)
		}
		if *output != "" {
			fmt.Fprintf(rep, "written to %s\n", *output)
		}
		switch {
		case !valid:
			fmt.Fprintln(rep, "result: the translation is not a valid LOTUS configuration (this is a bug in the translator):")
			for _, d := range diags {
				printDiagnostic(rep, d)
			}
		case res.NeedsReview():
			fmt.Fprintf(rep, "result: %d place(s) need your review; each is marked with a comment in the output. Then run `lotus config validate` and `lotus config explain` on the result\n",
				counts[nginxconv.Unsupported]+counts[nginxconv.Approximated])
		default:
			fmt.Fprintln(rep, "result: everything was translated; run `lotus config validate` and `lotus config explain` on the result")
		}
	}
	switch {
	case !valid:
		return exitInvalidConfig
	case res.NeedsReview():
		return exitNotSupported
	}
	return exitOK
}

// checkText validates configuration text by writing it to a temporary file.
func checkText(text string) (bool, []diag.Diagnostic) {
	dir, err := os.MkdirTemp("", "lotus-convert-")
	if err != nil {
		return true, nil // can't check; don't claim a problem
	}
	defer os.RemoveAll(dir)
	p := filepath.Join(dir, "converted.lotus")
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		return true, nil
	}
	res, diags := config.Check(p, loader.Options{})
	return res != nil, diags
}
