// Package docgen turns the directive registry into the configuration
// reference (docs/configuration/directives). The pages are generated, so they
// can't drift from the code: a test fails when the files on disk differ from
// what this package produces (run `make docs` to update them).
package docgen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/laikait/lotus/internal/config/schema"
)

const banner = "<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->\n"

// variant is one definition of a directive in one or more contexts.
type variant struct {
	usage    string
	contexts []string
	d        *schema.Directive
}

// Generate returns the pages of the reference, by file name: one per directive
// name, and index.md.
func Generate(reg *schema.Registry) map[string]string {
	byName := map[string][]*variant{}
	reg.All(func(ctx schema.Context, d *schema.Directive) {
		for _, v := range byName[d.Name] {
			if same(v.d, d) {
				v.contexts = append(v.contexts, string(ctx))
				return
			}
		}
		byName[d.Name] = append(byName[d.Name], &variant{usage: d.Usage(), contexts: []string{string(ctx)}, d: d})
	})

	pages := map[string]string{}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		pages[n+".md"] = page(n, byName[n])
	}
	pages["index.md"] = index(reg, names, byName)
	return pages
}

// same reports whether two definitions document the same thing, so they can
// share one section (the directive is then listed with all its contexts).
func same(a, b *schema.Directive) bool {
	return a.Usage() == b.Usage() && a.Description == b.Description && a.Security == b.Security && a.Default == b.Default &&
		a.Card == b.Card && a.Placeholders == b.Placeholders && a.Secret == b.Secret && a.TenantForbidden == b.TenantForbidden &&
		a.Defines == b.Defines && a.Opens == b.Opens
}

func codes(l []string) string {
	q := make([]string, len(l))
	for i, s := range l {
		q[i] = "`" + s + "`"
	}
	return strings.Join(q, ", ")
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func page(name string, vs []*variant) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# `%s`\n\n%s\n", name, banner)
	for i, v := range vs {
		d := v.d
		sort.Strings(v.contexts)
		if len(vs) > 1 {
			fmt.Fprintf(&b, "## In %s\n\n", codes(v.contexts))
		}
		fmt.Fprintf(&b, "```text\n%s\n```\n\n", v.usage)
		b.WriteString("| | |\n|---|---|\n")
		if len(vs) == 1 {
			fmt.Fprintf(&b, "| **Contexts** | %s |\n", codes(v.contexts))
		}
		if len(d.Args) > 0 {
			var args []string
			for _, a := range d.Args {
				n := a.Name
				if n == "" {
					n = a.Type.Name()
				}
				s := fmt.Sprintf("`%s`: %s", n, a.Type.Name())
				switch {
				case a.Variadic:
					s += " (repeats)"
				case a.Optional:
					s += " (optional)"
				}
				args = append(args, s)
			}
			fmt.Fprintf(&b, "| **Arguments** | %s |\n", strings.Join(args, "<br>"))
		}
		switch d.Block {
		case schema.BlockRequired:
			fmt.Fprintf(&b, "| **Block** | required%s |\n", opens(d))
		case schema.BlockOptional:
			fmt.Fprintf(&b, "| **Block** | optional%s |\n", opens(d))
		}
		switch d.Card {
		case schema.Single:
			b.WriteString("| **How often** | at most once |\n")
		case schema.Repeated:
			b.WriteString("| **How often** | any number of times, in order |\n")
		case schema.Keyed:
			s := "any number of times; the first argument is a unique name"
			if d.Defines != "" {
				s += " (defines a `" + d.Defines + "`)"
			}
			fmt.Fprintf(&b, "| **How often** | %s |\n", s)
		}
		fmt.Fprintf(&b, "| **Default** | %s |\n", cell(orNone(d.Default)))
		fmt.Fprintf(&b, "| **Request-time placeholders** | %s |\n", yesNo(d.Placeholders))
		if d.Secret {
			b.WriteString("| **Secret** | yes: redacted in every output |\n")
		}
		if d.TenantForbidden {
			b.WriteString("| **Who can set it** | administrators only: refused in files a site includes for its tenant |\n")
		} else {
			b.WriteString("| **Who can set it** | anyone who can edit the file |\n")
		}
		fmt.Fprintf(&b, "| **Since** | LOTUS %s, config language 1 |\n", orNone(d.Since))
		b.WriteString("\n### Description\n\n" + strings.TrimSpace(d.Description) + "\n\n")
		b.WriteString("### Security\n\n" + strings.TrimSpace(d.Security) + "\n")
		if i < len(vs)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// cell makes text safe inside a table cell.
func cell(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", "&#124;"), "\n", " ")
}

func opens(d *schema.Directive) string {
	if d.Opens == "" {
		return ""
	}
	return ", with the directives of the `" + string(d.Opens) + "` context"
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, ". "); i >= 0 {
		return s[:i+1]
	}
	return s
}

func index(reg *schema.Registry, names []string, byName map[string][]*variant) string {
	var b strings.Builder
	b.WriteString("# Configuration reference\n\n" + banner + "\n")
	b.WriteString("Every directive LOTUS understands, generated from the same registry the validator uses. See [the grammar](../grammar.md) for the syntax, and `lotus config validate` to check a file.\n\n")

	// How blocks nest.
	b.WriteString("## Where directives go\n\n```text\n")
	var walk func(ctx schema.Context, depth int, seen map[schema.Context]bool)
	walk = func(ctx schema.Context, depth int, seen map[schema.Context]bool) {
		cs := reg.Context(ctx)
		if cs == nil || seen[ctx] {
			return
		}
		seen[ctx] = true
		defer delete(seen, ctx)
		for _, n := range cs.Names() {
			d := cs.Directives[n]
			if d.Opens == "" {
				continue
			}
			fmt.Fprintf(&b, "%s%s\n", strings.Repeat("    ", depth), d.Name)
			walk(d.Opens, depth+1, seen)
		}
	}
	walk(schema.CtxRoot, 0, map[schema.Context]bool{})
	b.WriteString("```\n\nA directive that appears in several places (for example `compression`, `cache` or `max_request_body`) has one page that lists them.\n\n")

	b.WriteString("## All directives\n\n| Directive | What it does | Where |\n|---|---|---|\n")
	for _, n := range names {
		vs := byName[n]
		var ctxs []string
		seen := map[string]bool{}
		for _, v := range vs {
			for _, c := range v.contexts {
				if !seen[c] {
					seen[c] = true
					ctxs = append(ctxs, c)
				}
			}
		}
		sort.Strings(ctxs)
		fmt.Fprintf(&b, "| [`%s`](%s.md) | %s | %s |\n", n, n, cell(firstSentence(vs[0].d.Description)), codes(ctxs))
	}
	return b.String()
}
