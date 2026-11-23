package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/laikait/lotus/internal/config"
	"github.com/laikait/lotus/internal/config/loader"
	"github.com/laikait/lotus/internal/explain"
)

// headerFlags collects repeated --header "Name: value" options.
type headerFlags http.Header

func (h headerFlags) String() string { return "" }
func (h headerFlags) Set(v string) error {
	name, value, ok := strings.Cut(v, ":")
	if !ok || strings.TrimSpace(name) == "" {
		return fmt.Errorf("want \"Name: value\", got %q", v)
	}
	http.Header(h).Add(strings.TrimSpace(name), strings.TrimSpace(value))
	return nil
}

// cmdConfigExplain shows what the server would do with one request.
func cmdConfigExplain(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lotus config explain", flag.ContinueOnError)
	fs.SetOutput(stderr)
	method := fs.String("method", "GET", "request method")
	listener := fs.String("listener", "", "listener to use (default: the first with a site for the host)")
	clientIP := fs.String("client-ip", "", "client address after trusted_proxies, for client_ip conditions")
	jsonOut := fs.Bool("json", false, "print the result as JSON")
	noFiles := fs.Bool("no-files", false, "don't look at the file system")
	insecure := fs.Bool("insecure-includes", false, "skip permission checks on included files (development only)")
	headers := headerFlags(http.Header{})
	fs.Var(headers, "header", `request header "Name: value" (repeatable)`)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: lotus config explain [options] <file> <url>")
		fmt.Fprintln(stderr, `  <url> is a full URL (http://example.com/path?x=1), or a path with --header "Host: example.com"`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 2 {
		fs.Usage()
		return exitUsage
	}

	req, err := buildExplainRequest(fs.Arg(1), *method, *listener, *clientIP, http.Header(headers))
	if err != nil {
		fmt.Fprintf(stderr, "lotus: %v\n", err)
		return exitUsage
	}

	res, diags := config.Check(fs.Arg(0), loader.Options{InsecureIncludes: *insecure})
	if res == nil {
		for _, d := range diags {
			printDiagnostic(stderr, d)
		}
		fmt.Fprintf(stderr, "%s: configuration is invalid; nothing to explain\n", fs.Arg(0))
		return exitInvalidConfig
	}

	var files explain.Files
	if !*noFiles {
		disk := explain.NewDiskFiles()
		defer disk.Close()
		files = disk
	}
	rep := explain.Explain(res.Config, req, files)

	if *jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintf(stderr, "lotus: %v\n", err)
			return exitUsage
		}
		return exitOK
	}
	explain.WriteText(stdout, rep)
	return exitOK
}

func buildExplainRequest(rawURL, method, listener, clientIP string, hdr http.Header) (explain.Request, error) {
	req := explain.Request{Method: strings.ToUpper(method), Listener: listener, Header: hdr, Scheme: "http"}
	u, err := url.Parse(rawURL)
	if err != nil {
		return req, fmt.Errorf("bad URL: %v", err)
	}
	req.Host = u.Host
	if u.Scheme != "" {
		if u.Scheme != "http" && u.Scheme != "https" {
			return req, fmt.Errorf("the URL scheme must be http or https, not %q", u.Scheme)
		}
		req.Scheme = u.Scheme
	}
	if req.Host == "" {
		req.Host = hdr.Get("Host")
	}
	if req.Host == "" {
		return req, fmt.Errorf(`the URL has no host: give a full URL, or a path with --header "Host: example.com"`)
	}
	req.Path, req.RawQuery = u.Path, u.RawQuery
	if req.Path == "" {
		req.Path = "/"
	}
	if clientIP != "" {
		a, err := netip.ParseAddr(clientIP)
		if err != nil {
			return req, fmt.Errorf("bad --client-ip: %v", err)
		}
		req.ClientIP = a
	}
	return req, nil
}
