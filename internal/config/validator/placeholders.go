package validator

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/laikait/lotus/internal/config/ast"
	"github.com/laikait/lotus/internal/config/diag"
	"github.com/laikait/lotus/internal/config/schema"
)

// Request-time placeholders (grammar §6.3): {name} or {name.sub}.
var placeholderRe = regexp.MustCompile(`\{([a-z_][a-z0-9_]*)(?:\.([A-Za-z0-9_-]+))?\}`)

var plainPlaceholders = []string{
	"uri", "path", "query", "host", "scheme", "method", "remote_ip", "request_id",
}

var keyedPlaceholders = []string{"header", "query", "cookie", "re"}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func knownPlaceholder(name, sub string) bool {
	if sub == "" {
		return contains(plainPlaceholders, name)
	}
	if !contains(keyedPlaceholders, name) {
		return false
	}
	if name == "re" { // regex capture group: {re.1}
		for _, r := range sub {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// placeholders reports placeholders in strings that must not have them, and
// unknown placeholders in strings that may. Text marked Escaped (from \{ \},
// env() or file()) is never scanned.
func (v *validator) placeholders(directive string, allowed bool, val ast.Value) {
	switch x := val.(type) {
	case *ast.List:
		for _, item := range x.Items {
			v.placeholders(directive, allowed, item)
		}
	case *ast.String:
		for _, part := range x.Parts {
			if part.Kind != ast.PartText || part.Escaped {
				continue
			}
			for _, m := range placeholderRe.FindAllStringSubmatch(part.Text, -1) {
				v.checkPlaceholder(directive, allowed, x, m[0], m[1], m[2])
			}
		}
	}
}

func (v *validator) checkPlaceholder(directive string, allowed bool, s *ast.String, text, name, sub string) {
	known := knownPlaceholder(name, sub)
	switch {
	case !allowed && known:
		v.Error(s.Pos, diag.CodePlaceholderNotAllowed,
			fmt.Sprintf("%q does not expand request-time placeholders, but this value contains %s", directive, text),
			`write \{ and \} for literal braces, or use a directive that supports placeholders (try_files, return)`)
	case allowed && !known:
		hint := "known placeholders: " + strings.Join(allPlaceholderNames(), " ")
		if sg := schema.Suggest(name, plainPlaceholders); sg != "" && sub == "" {
			hint = "did you mean {" + strings.Trim(sg, `"`) + "}?"
		}
		v.Error(s.Pos, diag.CodeUnknownPlaceholder, fmt.Sprintf("unknown placeholder %s", text), hint)
	}
}

func allPlaceholderNames() []string {
	var out []string
	for _, n := range plainPlaceholders {
		out = append(out, "{"+n+"}")
	}
	for _, n := range keyedPlaceholders {
		out = append(out, "{"+n+".<name>}")
	}
	return out
}
