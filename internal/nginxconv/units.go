package nginxconv

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// quote writes s as a LOTUS double-quoted string. Literal braces are escaped so
// that text such as "{x}" is never taken for a placeholder; placeholders that
// the translator itself produced are passed as parts (see tmpl).
func quote(s string) string { return tmpl([]part{{text: s}}) }

type part struct {
	text string
	ph   bool // text is a placeholder such as {host}, written as it is
}

func tmpl(parts []part) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, p := range parts {
		if p.ph {
			b.WriteString(p.text)
			continue
		}
		for i := 0; i < len(p.text); i++ {
			c := p.text[i]
			switch {
			case c == '\\':
				b.WriteString(`\\`)
			case c == '"':
				b.WriteString(`\"`)
			case c == '{':
				b.WriteString(`\{`)
			case c == '}':
				b.WriteString(`\}`)
			case c == '$' && i+1 < len(p.text) && p.text[i+1] == '{':
				b.WriteString(`\$`)
			case c == '\n':
				b.WriteString(`\n`)
			case c == '\t':
				b.WriteString(`\t`)
			case c == '\r':
				b.WriteString(`\r`)
			case c < 0x20 || c == 0x7f:
				fmt.Fprintf(&b, `\u{%x}`, c)
			default:
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// raw writes s as a single-quoted string (no escapes), for regular expressions
// and paths. ok is false when s holds a character a raw string can't.
func raw(s string) (string, bool) {
	if strings.ContainsAny(s, "'\n\r") {
		return "", false
	}
	for _, c := range []byte(s) {
		if c < 0x20 && c != '\t' || c == 0x7f {
			return "", false
		}
	}
	return "'" + s + "'", true
}

var timePart = regexp.MustCompile(`^(\d+)(ms|s|m|h|d|w|M|y)?`)

// duration converts an Nginx time ("30s", "1h30m", "10") to a LOTUS duration.
func duration(s string) (string, bool) {
	var ms int64
	rest := s
	if rest == "" {
		return "", false
	}
	for rest != "" {
		m := timePart.FindStringSubmatch(rest)
		if m == nil {
			return "", false
		}
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || n > 1<<40 {
			return "", false
		}
		unit := map[string]int64{"ms": 1, "": 1000, "s": 1000, "m": 60000, "h": 3600000, "d": 86400000,
			"w": 7 * 86400000, "M": 30 * 86400000, "y": 365 * 86400000}[m[2]]
		ms += n * unit
		rest = rest[len(m[0]):]
	}
	return formatMillis(ms), true
}

func formatMillis(ms int64) string {
	if ms == 0 {
		return "0s"
	}
	var b strings.Builder
	for _, u := range []struct {
		name string
		ms   int64
	}{{"d", 86400000}, {"h", 3600000}, {"m", 60000}, {"s", 1000}, {"ms", 1}} {
		if n := ms / u.ms; n > 0 {
			fmt.Fprintf(&b, "%d%s", n, u.name)
			ms -= n * u.ms
		}
	}
	return b.String()
}

var sizeRe = regexp.MustCompile(`^(\d+)([kKmMgG]?)$`)

// size converts an Nginx size ("10m", "128k", "1024") to LOTUS's binary units.
func size(s string) (string, int64, bool) {
	m := sizeRe.FindStringSubmatch(s)
	if m == nil {
		return "", 0, false
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || n > 1<<40 {
		return "", 0, false
	}
	switch strings.ToLower(m[2]) {
	case "k":
		return fmt.Sprintf("%dkib", n), n << 10, true
	case "m":
		return fmt.Sprintf("%dmib", n), n << 20, true
	case "g":
		return fmt.Sprintf("%dgib", n), n << 30, true
	}
	return fmt.Sprintf("%db", n), n, true
}

// Variable contexts decide what a few Nginx variables mean in LOTUS.
const (
	// ctxRequest is a value LOTUS builds from the request itself: a return
	// target or a header value. {uri} there is the whole request URI.
	ctxRequest = iota
	// ctxInternal is a try_files entry or rewrite target, where {uri} is the
	// path alone and the query string is carried by {query}.
	ctxInternal
)

var varName = regexp.MustCompile(`\$(?:\{([A-Za-z_][A-Za-z0-9_]*)\}|([A-Za-z_][A-Za-z0-9_]*|[0-9]))`)

// translate turns text with Nginx variables into LOTUS parts. unsupported
// lists the variables that have no equivalent; the caller refuses the value.
func translate(s string, ctx int) (parts []part, unsupported []string) {
	last := 0
	for _, m := range varName.FindAllStringSubmatchIndex(s, -1) {
		if m[0] > last {
			parts = append(parts, part{text: s[last:m[0]]})
		}
		var name string
		if m[2] >= 0 {
			name = s[m[2]:m[3]]
		} else {
			name = s[m[4]:m[5]]
		}
		last = m[1]
		if ph, ok := variable(name, ctx); ok {
			parts = append(parts, ph...)
		} else {
			unsupported = append(unsupported, "$"+name)
		}
	}
	if last < len(s) {
		parts = append(parts, part{text: s[last:]})
	}
	return parts, unsupported
}

func ph(text string) []part { return []part{{text: text, ph: true}} }

func variable(name string, ctx int) ([]part, bool) {
	switch name {
	case "uri", "document_uri":
		if ctx == ctxInternal {
			return ph("{uri}"), true
		}
		return ph("{path}"), true
	case "request_uri":
		if ctx == ctxInternal {
			return []part{{text: "{uri}", ph: true}, {text: "?"}, {text: "{query}", ph: true}}, true
		}
		return ph("{uri}"), true
	case "args", "query_string":
		return ph("{query}"), true
	case "host", "http_host", "server_name":
		if name == "server_name" {
			return nil, false
		}
		return ph("{host}"), true
	case "scheme":
		return ph("{scheme}"), true
	case "request_method":
		return ph("{method}"), true
	case "remote_addr", "realip_remote_addr":
		return ph("{remote_ip}"), true
	case "request_id":
		return ph("{request_id}"), true
	}
	if len(name) == 1 && name[0] >= '0' && name[0] <= '9' {
		return ph("{re." + name + "}"), true
	}
	if rest, ok := strings.CutPrefix(name, "http_"); ok && rest != "" {
		return ph("{header." + headerName(rest) + "}"), true
	}
	if rest, ok := strings.CutPrefix(name, "arg_"); ok && rest != "" {
		return ph("{query." + rest + "}"), true
	}
	if rest, ok := strings.CutPrefix(name, "cookie_"); ok && rest != "" {
		return ph("{cookie." + rest + "}"), true
	}
	return nil, false
}

// headerName turns the Nginx variable form (user_agent) into a header name.
func headerName(v string) string {
	words := strings.Split(v, "_")
	for i, w := range words {
		if w != "" {
			words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
		}
	}
	return strings.Join(words, "-")
}
