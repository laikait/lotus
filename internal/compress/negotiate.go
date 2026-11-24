// Package compress compresses responses on the fly (gzip, Brotli, Zstandard)
// and negotiates the coding with the client (docs/architecture/compression.md).
//
// It is careful about what it touches: only responses of the configured media
// types, only whole 200-class bodies the backend left uncompressed, never a
// response that is already encoded, partial, marked no-transform or an
// upgrade. It never makes a response worse: below a minimum size, or when the
// server is already compressing as much as it can afford, the response goes out
// as it was.
package compress

import (
	"sort"
	"strconv"
	"strings"
)

// The codings LOTUS can produce.
const (
	Brotli = "br"
	Zstd   = "zstd"
	Gzip   = "gzip"
)

// Codings lists every coding that can be configured.
var Codings = []string{Brotli, Zstd, Gzip}

// Negotiate returns the codings among offered that the Accept-Encoding header
// accepts, best first: by the client's weight, and for equal weights in the
// order of offered (the server's preference). A coding the header doesn't name
// is not accepted unless "*" is there; a weight of 0 refuses it.
func Negotiate(header string, offered []string) []string {
	if header == "" || len(offered) == 0 {
		return nil
	}
	weights := map[string]float64{}
	wildcard, hasWildcard := 0.0, false
	for _, part := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(part, ";")
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			if k, v, ok := strings.Cut(p, "="); ok && strings.EqualFold(strings.TrimSpace(k), "q") {
				if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					q = f
				} else {
					q = 0 // an unreadable weight is no offer
				}
			}
		}
		if q < 0 {
			q = 0
		}
		if name == "x-gzip" {
			name = Gzip
		}
		if name == "*" {
			wildcard, hasWildcard = q, true
			continue
		}
		weights[name] = q
	}
	type scored struct {
		coding string
		q      float64
		rank   int
	}
	var out []scored
	for i, c := range offered {
		q, ok := weights[c]
		if !ok {
			if !hasWildcard {
				continue
			}
			q = wildcard
		}
		if q > 0 {
			out = append(out, scored{c, q, i})
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].q != out[b].q {
			return out[a].q > out[b].q
		}
		return out[a].rank < out[b].rank
	})
	list := make([]string, len(out))
	for i, s := range out {
		list[i] = s.coding
	}
	return list
}
