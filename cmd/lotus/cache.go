package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/laikait/lotus/internal/control"
)

const cacheUsage = `usage: lotus cache status [--zone NAME] [--json] [--control SOCKET]
       lotus cache purge [--zone NAME] [--host HOST] (--path PATH | --prefix PREFIX | --all) [--control SOCKET]`

// cmdCache shows and empties the response cache zones of the running server.
func cmdCache(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, cacheUsage)
		return exitUsage
	}
	switch args[0] {
	case "status":
		return cmdCacheStatus(args[1:], stdout, stderr)
	case "purge":
		return cmdCachePurge(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "lotus: unknown cache command %q\n%s\n", args[0], cacheUsage)
	return exitUsage
}

func cmdCacheStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lotus cache status", flag.ContinueOnError)
	socket := controlFlags(fs)
	zone := fs.String("zone", "", "show only this cache zone")
	jsonOut := fs.Bool("json", false, "print the zones as JSON")
	if !parseNoArgs(fs, args, stderr, "lotus cache status [--zone NAME] [--json] [--control SOCKET]") {
		return exitUsage
	}
	c, code := dial(*socket, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	var zones []control.CacheZone
	if err := c.Call(control.MethodCacheStatus, control.CacheStatusParams{Zone: *zone}, &zones); err != nil {
		fmt.Fprintf(stderr, "lotus: %v\n", err)
		return exitCodeFor(err)
	}
	writeCacheStatus(stdout, zones, *jsonOut)
	return exitOK
}

// hitRate is the share of requests answered from the cache, among those that
// could have been (hits and misses; requests that bypass it don't count).
func hitRate(z control.CacheZone) (float64, bool) {
	total := z.Hits + z.Misses
	if total == 0 {
		return 0, false
	}
	return 100 * float64(z.Hits) / float64(total), true
}

type cacheRow struct {
	control.CacheZone
	HitRate *float64 `json:"hit_rate_percent,omitempty"`
}

func writeCacheStatus(w io.Writer, zones []control.CacheZone, asJSON bool) {
	if asJSON {
		rows := make([]cacheRow, len(zones))
		for i, z := range zones {
			rows[i] = cacheRow{CacheZone: z}
			if r, ok := hitRate(z); ok {
				rows[i].HitRate = &r
			}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rows)
		return
	}
	if len(zones) == 0 {
		fmt.Fprintln(w, "no cache zones: the configuration has no cache block")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ZONE\tENTRIES\tSIZE\tLIMIT\tHIT RATE\tHITS\tMISSES\tSTALE\tBYPASS\tEVICTED")
	for _, z := range zones {
		rate := "-"
		if r, ok := hitRate(z); ok {
			rate = fmt.Sprintf("%.1f%%", r)
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\n",
			z.Zone, z.Entries, humanBytes(z.Bytes), humanBytes(z.MaxBytes), rate, z.Hits, z.Misses, z.Stale, z.Bypass, z.Evictions)
	}
	tw.Flush()
}

// humanBytes writes a size with a binary unit.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}

func cmdCachePurge(args []string, stdout, stderr io.Writer) int {
	const usage = "lotus cache purge [--zone NAME] [--host HOST] (--path PATH | --prefix PREFIX | --all) [--control SOCKET]"
	fs := flag.NewFlagSet("lotus cache purge", flag.ContinueOnError)
	socket := controlFlags(fs)
	zone := fs.String("zone", "", "the cache zone (not needed when there is only one)")
	host := fs.String("host", "", "only for this site's host name")
	path := fs.String("path", "", "remove one URL path (every query string), such as /news/today")
	prefix := fs.String("prefix", "", "remove every URL path that starts with this, such as /news/")
	all := fs.Bool("all", false, "remove everything in the zone")
	if !parseNoArgs(fs, args, stderr, usage) {
		return exitUsage
	}
	targets := 0
	for _, set := range []bool{*all, *path != "", *prefix != ""} {
		if set {
			targets++
		}
	}
	switch {
	case targets != 1:
		fmt.Fprintln(stderr, "lotus: say what to purge with exactly one of --path, --prefix or --all")
		return exitUsage
	case *all && *host != "":
		fmt.Fprintln(stderr, "lotus: --all removes everything; --host only goes with --path or --prefix")
		return exitUsage
	}
	for _, p := range []string{*path, *prefix} {
		if p != "" && !strings.HasPrefix(p, "/") {
			fmt.Fprintf(stderr, "lotus: %q is not a URL path: it must start with /\n", p)
			return exitUsage
		}
	}
	c, code := dial(*socket, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	var res control.CachePurgeResult
	params := control.CachePurgeParams{Zone: *zone, Host: *host, Path: *path, Prefix: *prefix, All: *all}
	if err := c.Call(control.MethodCachePurge, params, &res); err != nil {
		fmt.Fprintf(stderr, "lotus: %v\n", err)
		return exitCodeFor(err)
	}
	noun := "entries"
	if res.Purged == 1 {
		noun = "entry"
	}
	fmt.Fprintf(stdout, "purged %d %s\n", res.Purged, noun)
	return exitOK
}
