package router

import (
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/laikait/lotus/internal/config/model"
)

const LiteralBrace = model.LiteralBrace

// Vars supplies placeholder values for one request.
type Vars struct {
	URI       string // escaped path plus "?query" when there is one
	Path      string // decoded, normalized path
	Query     string // raw query string, without '?'
	Host      string
	Scheme    string
	Method    string
	RemoteIP  string
	RequestID string
	Header    func(name string) string
	QueryArg  func(name string) string
	Cookie    func(name string) string
	Captures  []string // regex submatches of the chosen route
}

// ErrUnsafeValue means a placeholder expanded to text containing CR, LF or
// NUL. Such a value could split a header or smuggle a second response.
var ErrUnsafeValue = errors.New("placeholder value contains a control character")

var placeholderRe = regexp.MustCompile(`\{([a-z_][a-z0-9_]*)(?:\.([A-Za-z0-9_-]+))?\}`)

// Expand replaces request-time placeholders in tmpl. Unknown placeholders are
// left as written (the validator rejects them before the server starts).
// Values that contain CR, LF or NUL make Expand fail with ErrUnsafeValue.
func Expand(tmpl string, v *Vars) (string, error) {
	if !strings.ContainsAny(tmpl, "{\x00") {
		return tmpl, nil
	}
	var out strings.Builder
	var bad bool
	rest := tmpl
	for len(rest) > 0 {
		i := strings.IndexByte(rest, '{')
		if i < 0 {
			out.WriteString(strings.ReplaceAll(rest, LiteralBrace, ""))
			break
		}
		// A '{' right after the marker is literal.
		if i > 0 && rest[i-1] == 0 {
			out.WriteString(strings.ReplaceAll(rest[:i-1], LiteralBrace, ""))
			out.WriteByte('{')
			rest = rest[i+1:]
			continue
		}
		out.WriteString(strings.ReplaceAll(rest[:i], LiteralBrace, ""))
		loc := placeholderRe.FindStringSubmatchIndex(rest[i:])
		if loc == nil || loc[0] != 0 {
			out.WriteByte('{')
			rest = rest[i+1:]
			continue
		}
		whole := rest[i : i+loc[1]]
		name := rest[i+loc[2] : i+loc[3]]
		sub := ""
		if loc[4] >= 0 {
			sub = rest[i+loc[4] : i+loc[5]]
		}
		val, known := v.lookup(name, sub)
		if !known {
			out.WriteString(whole)
		} else {
			if strings.ContainsAny(val, "\r\n\x00") {
				bad = true
			}
			out.WriteString(val)
		}
		rest = rest[i+loc[1]:]
	}
	if bad {
		return "", ErrUnsafeValue
	}
	return out.String(), nil
}

func (v *Vars) lookup(name, sub string) (string, bool) {
	switch name {
	case "uri":
		return v.URI, sub == ""
	case "path":
		return v.Path, sub == ""
	case "query":
		if sub == "" {
			return v.Query, true
		}
		if v.QueryArg != nil {
			return v.QueryArg(sub), true
		}
		return "", true
	case "host":
		return v.Host, sub == ""
	case "scheme":
		return v.Scheme, sub == ""
	case "method":
		return v.Method, sub == ""
	case "remote_ip":
		return v.RemoteIP, sub == ""
	case "request_id":
		return v.RequestID, sub == ""
	case "header":
		if sub != "" && v.Header != nil {
			return v.Header(sub), true
		}
		return "", sub != ""
	case "cookie":
		if sub != "" && v.Cookie != nil {
			return v.Cookie(sub), true
		}
		return "", sub != ""
	case "re":
		n, err := strconv.Atoi(sub)
		if err != nil || sub == "" {
			return "", false
		}
		if n < len(v.Captures) {
			return v.Captures[n], true
		}
		return "", true
	}
	return "", false
}

// EscapedURI returns the request URI form of a path and raw query.
func EscapedURI(p, rawQuery string) string {
	u := (&url.URL{Path: p}).EscapedPath()
	if rawQuery != "" {
		u += "?" + rawQuery
	}
	return u
}

// ExpandInternal expands a template that names a URI to route to internally
// (a try_files entry or a rewrite target). The template is split at its first
// '?' before anything is expanded: the path part expands to decoded text, with
// {uri} meaning the path alone, and the query part expands to raw query text.
// Nothing is percent-decoded afterwards, so a path such as /a%2520b is never
// decoded twice, and a '?' that came from a request value can't start a query.
func ExpandInternal(tmpl string, v *Vars) (path, query string, hasQuery bool, err error) {
	pathT, queryT, hasQuery := strings.Cut(tmpl, "?")
	pv := *v
	pv.URI = v.Path
	if path, err = Expand(pathT, &pv); err != nil {
		return "", "", false, err
	}
	if hasQuery {
		if query, err = Expand(queryT, v); err != nil {
			return "", "", false, err
		}
	}
	return path, query, hasQuery, nil
}
