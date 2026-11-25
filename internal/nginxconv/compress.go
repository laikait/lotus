package nginxconv

import (
	"strconv"
	"strings"
)

// compCfg is the effective gzip configuration of a context.
type compCfg struct {
	types  []string // lowercase media types; nil means LOTUS's default list
	min    string
	level  string
	static bool
}

func (a *compCfg) equal(b *compCfg) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.min == b.min && a.level == b.level && a.static == b.static && strings.Join(a.types, ",") == strings.Join(b.types, ",")
}

// compression works out what `gzip on` and its companions mean for a context.
// nil means compression is off there.
func (c *conv) compression(sc *scope, _ *compCfg) *compCfg {
	g := sc.one("gzip")
	if g == nil || len(g.Args) != 1 || g.Args[0] != "on" {
		if g != nil {
			c.ok(g)
		}
		return nil
	}
	cfg := &compCfg{min: "20b", level: "fast"}
	types := []string{"text/html"} // Nginx always compresses it
	if nodes := sc.find("gzip_types"); len(nodes) > 0 {
		for _, n := range nodes {
			for _, a := range n.Args {
				a = strings.ToLower(a)
				if a == "*" {
					c.flag(nil, n, Approximated, "Nginx compresses every type with *; LOTUS compresses its default list of text-like types")
					types = nil
					break
				}
				if types != nil {
					types = append(types, a)
				}
			}
			c.ok(n)
		}
	}
	// JavaScript has two media types in use, and Nginx's mime.types and LOTUS's
	// table may not agree on which one .js files get; compress both.
	if types != nil {
		has := map[string]bool{}
		for _, t := range types {
			has[t] = true
		}
		if has["application/javascript"] || has["text/javascript"] || has["application/x-javascript"] {
			types = append(types, "application/javascript", "text/javascript")
		}
	}
	cfg.types = types
	if n := sc.one("gzip_min_length"); n != nil && len(n.Args) == 1 {
		if s, _, ok := size(n.Args[0]); ok {
			cfg.min = s
			c.ok(n)
		}
	}
	if n := sc.one("gzip_comp_level"); n != nil && len(n.Args) == 1 {
		if l, err := strconv.Atoi(n.Args[0]); err == nil {
			switch {
			case l <= 3:
				cfg.level = "fast"
			case l <= 6:
				cfg.level = "default"
			default:
				cfg.level = "best"
			}
			c.ok(n)
		}
	}
	if n := sc.one("gzip_static"); n != nil && len(n.Args) == 1 {
		switch n.Args[0] {
		case "on", "always":
			cfg.static = true
			c.note(n, Translated, "precompressed copies are looked for as .gz, and also as .br and .zst")
		}
	}
	c.ok(g)
	return cfg
}

func (cfg *compCfg) write(o *out) {
	o.open("compression")
	o.line(`algorithms [ "gzip" ]`)
	if cfg.types != nil {
		o.line("types [ %s ]", joinQuoted(dedupe(cfg.types)))
	}
	o.line("min_size %s", cfg.min)
	o.line("level %s", cfg.level)
	if cfg.static {
		o.line("precompressed true")
	}
	o.close()
}

func dedupe(l []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range l {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
