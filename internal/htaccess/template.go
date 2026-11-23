package htaccess

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Request is what a .htaccess file can see of a request. The server fills it in.
type Request struct {
	Method     string
	Host       string // Host header, with any port
	Scheme     string // "http" or "https"
	Proto      string // "HTTP/1.1"
	Remote     netip.Addr
	ServerPort string
	// RequestLine is the first line the client sent ("GET /a?b HTTP/1.1"), for THE_REQUEST.
	RequestLine string

	// Path is the current URL path, decoded, and Query the raw query. They
	// change as rewrite rules fire (the server passes the new ones back in).
	Path  string
	Query string

	Header func(name string) string

	// Root is the document root with forward slashes, no trailing slash.
	Root string
	// Stat looks a URL path up below the document root with the server's file
	// rules (hidden files, symlink policy and so on).
	Stat func(urlPath string) (info FileInfo, ok bool)
}

// FileInfo is the result of Stat.
type FileInfo struct {
	IsDir bool
	Size  int64
}

// tpart is one piece of a template.
type tpart struct {
	kind int // tLit, tRule ($N), tCond (%N), tVar
	lit  string
	n    int
	name string // variable name, upper case, or "HTTP"/"ENV" with key
	key  string
}

const (
	tLit = iota
	tRule
	tCond
	tVar
)

// template is a string with $N, %N and %{VAR} placeholders.
type template []tpart

// knownVars are the server variables a template may use. Anything else is
// refused when the file is compiled, because expanding it to nothing would
// silently change what a rule means.
var knownVars = map[string]bool{
	"REQUEST_URI": true, "REQUEST_FILENAME": true, "SCRIPT_FILENAME": true, "DOCUMENT_ROOT": true,
	"QUERY_STRING": true, "REQUEST_METHOD": true, "REQUEST_SCHEME": true, "REQUEST_PROTOCOL": true,
	"SERVER_PROTOCOL": true, "HTTPS": true, "REMOTE_ADDR": true, "SERVER_NAME": true, "SERVER_PORT": true,
	"THE_REQUEST": true, "HTTP_HOST": true, "HTTP_USER_AGENT": true, "HTTP_REFERER": true,
	"HTTP_COOKIE": true, "HTTP_ACCEPT": true, "HTTP_FORWARDED": true, "HTTP_CONNECTION": true,
	"HTTP_ACCEPT_ENCODING": true, "HTTP_ACCEPT_LANGUAGE": true,
}

func parseTemplate(s string) (template, error) {
	var t template
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			t = append(t, tpart{kind: tLit, lit: lit.String()})
			lit.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && (s[i+1] == '$' || s[i+1] == '%'):
			lit.WriteByte(s[i+1])
			i++
		case c == '$' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9':
			flush()
			t = append(t, tpart{kind: tRule, n: int(s[i+1] - '0')})
			i++
		case c == '%' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9':
			flush()
			t = append(t, tpart{kind: tCond, n: int(s[i+1] - '0')})
			i++
		case c == '%' && i+1 < len(s) && s[i+1] == '{':
			end := strings.IndexByte(s[i:], '}')
			if end < 0 {
				return nil, fmt.Errorf("%%{ without a closing }")
			}
			body := s[i+2 : i+end]
			flush()
			name, key, hasKey := strings.Cut(body, ":")
			name = strings.ToUpper(name)
			switch {
			case hasKey && (name == "HTTP" || name == "ENV"):
				if key == "" {
					return nil, fmt.Errorf("%%{%s:} needs a name", name)
				}
				t = append(t, tpart{kind: tVar, name: name, key: key})
			case !hasKey && knownVars[name]:
				t = append(t, tpart{kind: tVar, name: name})
			default:
				return nil, fmt.Errorf("the variable %%{%s} is not supported", body)
			}
			i += end
		default:
			lit.WriteByte(c)
		}
	}
	flush()
	return t, nil
}

// evalCtx is the state while rules run.
type evalCtx struct {
	req      *Request
	path     string // current decoded URL path
	query    string
	ruleCaps []string // $N: captures of the rule's pattern
	condCaps []string // %N: captures of the last matching condition
	env      map[string]string
}

func capture(caps []string, n int) string {
	if n < len(caps) {
		return caps[n]
	}
	return ""
}

func (t template) expand(c *evalCtx) string {
	var b strings.Builder
	for _, p := range t {
		switch p.kind {
		case tLit:
			b.WriteString(p.lit)
		case tRule:
			b.WriteString(capture(c.ruleCaps, p.n))
		case tCond:
			b.WriteString(capture(c.condCaps, p.n))
		case tVar:
			b.WriteString(c.variable(p))
		}
	}
	return b.String()
}

func (c *evalCtx) header(name string) string {
	if c.req.Header == nil {
		return ""
	}
	return c.req.Header(name)
}

func (c *evalCtx) variable(p tpart) string {
	r := c.req
	switch p.name {
	case "HTTP":
		return c.header(p.key)
	case "ENV":
		return c.env[p.key]
	case "REQUEST_URI":
		return c.path
	case "REQUEST_FILENAME", "SCRIPT_FILENAME":
		return r.Root + c.path
	case "DOCUMENT_ROOT":
		return r.Root
	case "QUERY_STRING":
		return c.query
	case "REQUEST_METHOD":
		return r.Method
	case "REQUEST_SCHEME":
		return r.Scheme
	case "REQUEST_PROTOCOL", "SERVER_PROTOCOL":
		return r.Proto
	case "HTTPS":
		if r.Scheme == "https" {
			return "on"
		}
		return "off"
	case "REMOTE_ADDR":
		if r.Remote.IsValid() {
			return r.Remote.String()
		}
		return ""
	case "SERVER_NAME":
		return hostOnly(r.Host)
	case "SERVER_PORT":
		return r.ServerPort
	case "THE_REQUEST":
		return r.RequestLine
	case "HTTP_HOST":
		return r.Host
	case "HTTP_USER_AGENT":
		return c.header("User-Agent")
	case "HTTP_REFERER":
		return c.header("Referer")
	case "HTTP_COOKIE":
		return c.header("Cookie")
	case "HTTP_ACCEPT":
		return c.header("Accept")
	case "HTTP_FORWARDED":
		return c.header("Forwarded")
	case "HTTP_CONNECTION":
		return c.header("Connection")
	case "HTTP_ACCEPT_ENCODING":
		return c.header("Accept-Encoding")
	case "HTTP_ACCEPT_LANGUAGE":
		return c.header("Accept-Language")
	}
	return ""
}

func hostOnly(h string) string {
	if strings.HasPrefix(h, "[") {
		if i := strings.IndexByte(h, ']'); i > 0 {
			return h[1:i]
		}
	}
	if i := strings.LastIndexByte(h, ':'); i >= 0 && strings.Count(h, ":") == 1 {
		return h[:i]
	}
	return h
}

func atoi(s string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	return n, err == nil
}
