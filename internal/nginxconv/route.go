package nginxconv

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

type routeCtx struct {
	c         *conv
	root      string
	redirects []*redirect
}

// pinfo describes the location a nested one sits in.
type pinfo struct {
	kind string // "prefix", "exact", "regex"
	path string
}

func (c *conv) injectRedirects(o *out, rc *routeCtx) {
	if len(rc.redirects) == 0 {
		return
	}
	r := rc.redirects[0]
	o.open("match")
	o.line(`scheme "http"`)
	o.line("return %d %s", r.status, r.target)
	o.close()
}

// returnLines translates a return directive into LOTUS lines.
func (c *conv) returnLines(d *Node) ([]string, bool) {
	args := d.Args
	if len(args) == 0 || len(args) > 2 {
		return nil, false
	}
	code := 0
	rest := args
	if n, err := strconv.Atoi(args[0]); err == nil {
		code, rest = n, args[1:]
	} else if len(args) == 1 && (strings.HasPrefix(args[0], "http://") || strings.HasPrefix(args[0], "https://") || strings.HasPrefix(args[0], "$scheme")) {
		code = 302
	} else {
		return nil, false
	}
	if code < 100 || code > 599 {
		return nil, false
	}
	if len(rest) == 0 {
		return []string{fmt.Sprintf("return %d", code)}, true
	}
	parts, bad := translate(rest[0], ctxRequest)
	if len(bad) > 0 {
		c.note(d, Unsupported, "no equivalent for "+strings.Join(bad, ", "))
		return nil, false
	}
	return []string{fmt.Sprintf("return %d %s", code, tmpl(parts))}, true
}

// route writes a location as a route. It reports whether a route was written.
func (c *conv) route(o *out, sc *scope, loc *Node, parent *pinfo, rc *routeCtx, sib map[string]bool) bool {
	kind, path, head, ok := c.routeHead(o, loc)
	if !ok {
		return false
	}
	key := kind + "|" + path
	if kind == "regex" && strings.HasPrefix(head, "route ~* ") {
		key = "regexi|" + path
	}
	if sib[key] {
		c.flag(o, loc, Unsupported, "a location with the same path was already defined here; LOTUS does not allow two routes alike")
		return false
	}
	sib[key] = true
	if parent != nil && kind == "named" {
		c.flag(o, loc, Unsupported, "a named location must be directly inside the server")
		return false
	}
	if parent != nil && parent.kind != "prefix" {
		c.flag(o, loc, Unsupported, "only a prefix location can contain another location in LOTUS")
		return false
	}
	if parent != nil && (kind == "prefix" || kind == "exact") && !strings.HasPrefix(path, parent.path) {
		c.flag(o, loc, Unsupported, fmt.Sprintf("the path must start with that of the location it is in (%s)", parent.path))
		return false
	}
	lsc := sc.child(loc.Block)
	named := kind == "named"

	var (
		proxyName, phpName string
		tryFiles           []string
		rewrites           []string
		rets               []string
		extra              []string // lines written before the match blocks
		matches            [][]string
		errNodes           []*Node
		children           []*Node
		indexNode          *Node
		cacheNode          *Node
		cacheKind          string
		acl                []*Node
		limitReq           *Node
		maxBody            string
		proxyNode, phpNode *Node
		tryNode            *Node
	)
	for _, d := range loc.Block {
		switch d.Name {
		case "location":
			children = append(children, d)
		case "proxy_pass":
			proxyNode = d
			proxyName = c.proxyPass(o, lsc, d, kind, path)
		case "fastcgi_pass":
			phpNode = d
			phpName = c.fastcgiPass(o, lsc, d)
		case "try_files":
			tryNode = d
			tryFiles = c.tryFiles(o, d)
		case "return":
			if r, ok := c.returnLines(d); ok {
				if rets == nil {
					rets = r
				}
				c.ok(d)
			} else {
				c.flag(o, d, Unsupported, "this return can't be translated")
			}
		case "rewrite":
			if r := c.rewrite(o, d); r != "" {
				rewrites = append(rewrites, r)
			}
		case "error_page":
			errNodes = append(errNodes, d)
		case "index":
			indexNode = d
		case "client_max_body_size":
			if v, ok := c.bodyLimit(d); ok {
				maxBody = v
			}
		case "limit_req":
			limitReq = d
		case "if":
			if m := c.ifMatch(o, d); m != nil {
				matches = append(matches, m)
			}
		case "allow", "deny":
			acl = append(acl, d)
		case "proxy_cache", "fastcgi_cache":
			cacheNode, cacheKind = d, strings.TrimSuffix(d.Name, "_cache")
		case "root":
			if len(d.Args) == 1 && d.Args[0] == rc.root {
				c.ok(d)
			} else {
				c.flag(o, d, Unsupported, "a location can't have a document root of its own; LOTUS serves one root per site")
			}
		case "internal":
			if !named {
				c.flag(o, d, Unsupported, "a path route can't be limited to internal redirects; it also answers clients here")
			}
		case "gzip", "gzip_types", "gzip_min_length", "gzip_comp_level", "gzip_static":
			c.flag(o, d, Unsupported, "compression can't be switched per location; it is set for the site")
		case "include":
			c.includeNote(d)
		case "proxy_set_header", "proxy_connect_timeout", "proxy_read_timeout", "fastcgi_read_timeout", "fastcgi_connect_timeout",
			"proxy_cache_valid", "fastcgi_cache_valid", "proxy_cache_use_stale", "fastcgi_cache_use_stale", "proxy_cache_lock",
			"fastcgi_cache_lock", "proxy_cache_lock_timeout", "fastcgi_cache_lock_timeout", "proxy_cache_key", "fastcgi_cache_key",
			"proxy_cache_bypass", "fastcgi_cache_bypass", "proxy_no_cache", "fastcgi_no_cache", "limit_req_status":
			// read where they take effect
		default:
			c.contextDirective(o, lsc, d, "location")
		}
	}

	hasProxy, hasPHP := proxyName != "", phpName != ""
	// Nginx stops at the first return; nothing after it runs.
	if rets != nil {
		if hasProxy || hasPHP || tryFiles != nil {
			c.flag(o, loc, Ignored, "return answers first, so the proxy, php or try_files of this location never runs")
		}
		proxyName, phpName, tryFiles = "", "", nil
		hasProxy, hasPHP = false, false
	}
	if hasProxy && tryFiles != nil {
		c.flag(o, tryNode, Unsupported, "a route either proxies or serves files; use a separate location (and a named route) for each")
		tryFiles = nil
	}
	if hasPHP && tryFiles != nil {
		if len(tryFiles) == 2 && tryFiles[0] == `"{uri}"` && tryFiles[1] == `"=404"` {
			c.note(tryNode, Ignored, "php already answers 404 when the script file doesn't exist")
		} else {
			c.flag(o, tryNode, Unsupported, "try_files together with fastcgi_pass can't be combined; PHP runs only for files that exist")
		}
		tryFiles = nil
	}
	if hasProxy && hasPHP {
		c.flag(o, phpNode, Unsupported, "a route either proxies or runs PHP")
		phpName = ""
		hasPHP = false
	}

	// deny / allow
	var denyAll bool
	if len(acl) > 0 {
		m, all := c.accessRules(o, acl)
		if m != nil {
			matches = append([][]string{m}, matches...)
		}
		denyAll = all
	}
	if denyAll && rets == nil {
		rets = []string{"return 403"}
		proxyName, phpName, tryFiles = "", "", nil
		hasProxy, hasPHP = false, false
	}

	o.open("%s", head)
	// write what the location does
	for _, r := range rewrites {
		o.line("%s", r)
	}
	for _, r := range rets {
		o.line("%s", r)
	}
	if tryFiles != nil {
		o.line("try_files [ %s ]", strings.Join(tryFiles, ", "))
	}
	if hasProxy {
		o.line("proxy %s", quote(proxyName))
		c.proxyHeaders(o, lsc, proxyNode)
	}
	if hasPHP {
		o.line("php %s", quote(phpName))
	}
	if !named {
		c.errorPages(o, errNodes)
	} else if len(errNodes) > 0 {
		c.errorPages(o, errNodes)
	}
	if indexNode != nil {
		if files := c.indexFiles(indexNode); len(files) > 0 {
			o.line("index [ %s ]", joinQuoted(files))
			c.ok(indexNode)
		}
	}
	if maxBody != "" {
		o.line("max_request_body %s", maxBody)
	}
	if limitReq != nil {
		c.rateLimit(o, lsc, limitReq)
	}
	if cacheNode != nil {
		c.routeCache(o, lsc, cacheNode, cacheKind, hasProxy, hasPHP)
	}
	_ = extra
	if !named {
		rc.c.injectRedirects(o, rc)
	}
	for _, m := range matches {
		o.open("match")
		for _, l := range m {
			o.line("%s", l)
		}
		o.close()
	}
	me := &pinfo{kind: kind, path: path}
	childSeen := map[string]bool{}
	for _, ch := range children {
		c.route(o, lsc, ch, me, rc, childSeen)
	}
	o.close()
	c.ok(loc)
	return true
}

// routeHead translates the arguments of a location.
func (c *conv) routeHead(o *out, loc *Node) (kind, path, head string, ok bool) {
	args := loc.Args
	mod := ""
	switch len(args) {
	case 1:
		path = args[0]
	case 2:
		mod, path = args[0], args[1]
	default:
		c.flag(o, loc, Unsupported, "a location needs a path")
		return
	}
	switch {
	case mod == "" && strings.HasPrefix(path, "@"):
		name, valid := namedRoute(path)
		if !valid {
			c.flag(o, loc, Unsupported, fmt.Sprintf("the name %q can't be used (lowercase letters, digits, _ and -)", path))
			return
		}
		return "named", name, "route " + quote(name), true
	case mod == "~" || mod == "~*":
		pattern := path
		if mod == "~*" {
			pattern = "(?i)" + path
		}
		if _, err := regexp.Compile(pattern); err != nil {
			c.flag(o, loc, Unsupported, "the pattern is not valid in LOTUS (RE2: no backreferences or lookaround): "+strings.TrimPrefix(err.Error(), "error parsing regexp: "))
			return
		}
		r, valid := raw(path)
		if !valid {
			c.flag(o, loc, Unsupported, "a pattern with a single quote can't be written")
			return
		}
		return "regex", path, "route " + mod + " " + r, true
	case mod == "" || mod == "^~" || mod == "=":
		if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#{}\\\"'") || strings.Contains(path, "$") {
			c.flag(o, loc, Unsupported, fmt.Sprintf("the path %q can't be written as a LOTUS route path", path))
			return
		}
		switch mod {
		case "=":
			return "exact", path, "route = " + quote(path), true
		case "^~":
			return "prefix", path, "route ^~ " + quote(path), true
		}
		return "prefix", path, "route " + quote(path), true
	}
	c.flag(o, loc, Unsupported, fmt.Sprintf("the location modifier %q is not known", mod))
	return
}

// ---- proxy_pass, fastcgi_pass ----

var sanitizeRe = regexp.MustCompile(`[^A-Za-z0-9]+`)

func (c *conv) proxyPass(o *out, sc *scope, d *Node, kind, locPath string) string {
	if len(d.Args) != 1 {
		return ""
	}
	url := d.Args[0]
	if strings.Contains(url, "$") {
		c.flag(o, d, Unsupported, "proxy_pass with variables can't be translated; name the upstream")
		return ""
	}
	https := false
	switch {
	case strings.HasPrefix(url, "https://"):
		https, url = true, strings.TrimPrefix(url, "https://")
	case strings.HasPrefix(url, "http://"):
		url = strings.TrimPrefix(url, "http://")
	default:
		c.flag(o, d, Unsupported, "proxy_pass needs an http:// or https:// URL")
		return ""
	}
	hostport, uri := url, ""
	if i := strings.Index(url, "/"); i >= 0 && !strings.HasPrefix(url, "unix:") {
		hostport, uri = url[:i], url[i:]
	}
	var u *upstream
	if strings.HasPrefix(url, "unix:") {
		// http://unix:/path/to.sock:/uri
		rest := strings.TrimPrefix(url, "unix:")
		sock, after, _ := strings.Cut(rest, ":")
		uri = after
		hostport = "unix:" + sock
		u = c.directUpstream(hostport, hostport, false)
	} else if existing, ok := c.umap[hostport]; ok {
		u = existing
		if https {
			if len(u.backends) > 0 && !u.https && c.upstreamUsed(u) {
				c.flag(o, d, Approximated, "the upstream is used with http:// and https:// both; LOTUS gives an upstream one scheme")
			}
			u.https = true
		}
		u.used = true
	} else {
		host, port, err := net.SplitHostPort(hostport)
		if err != nil {
			host, port = hostport, "80"
			if https {
				port = "443"
			}
		}
		if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
			host = "[" + host + "]"
		}
		addr := host + ":" + port
		u = c.directUpstream("proxy-"+strings.Trim(sanitizeRe.ReplaceAllString(addr, "-"), "-"), addr, https)
	}
	if uri != "" && !((kind == "prefix") && uri == locPath) {
		c.flag(o, d, Approximated, fmt.Sprintf("proxy_pass has the URI %q, which replaces the matching part of the path in Nginx; LOTUS sends the path as it is, so the backend may see another path", uri))
	} else {
		c.ok(d)
	}
	c.proxyTimeouts(u, sc)
	return u.name
}

func (c *conv) upstreamUsed(u *upstream) bool { return u.used }

func (c *conv) directUpstream(name, addr string, https bool) *upstream {
	if u, ok := c.umap[name]; ok {
		u.used = true
		return u
	}
	u := &upstream{name: name, https: https, used: true, backends: []backend{{addr: addr, weight: 1}}}
	c.umap[name] = u
	c.ups = append(c.ups, u)
	return u
}

func (c *conv) proxyTimeouts(u *upstream, sc *scope) {
	set := func(dst *string, d *Node) {
		if d == nil || len(d.Args) != 1 {
			return
		}
		v, ok := duration(d.Args[0])
		if !ok {
			return
		}
		if *dst != "" && *dst != v {
			c.flag(nil, d, Approximated, "timeouts belong to the upstream in LOTUS and are shared by every location that uses it; the first value is kept")
			return
		}
		*dst = v
		c.ok(d)
	}
	set(&u.connect, sc.one("proxy_connect_timeout"))
	set(&u.response, sc.one("proxy_read_timeout"))
}

func (c *conv) proxyHeaders(o *out, sc *scope, pass *Node) {
	hostSet := false
	for _, d := range sc.find("proxy_set_header") {
		if len(d.Args) != 2 {
			continue
		}
		name, val := d.Args[0], d.Args[1]
		low := strings.ToLower(name)
		switch low {
		case "host":
			hostSet = true
			switch val {
			case "$host", "$http_host":
				c.note(d, Translated, "the backend gets the client's Host (proxy_host client is the default)")
			case "$proxy_host":
				o.line("proxy_host upstream")
				c.ok(d)
			default:
				c.flag(o, d, Approximated, "only the client's Host or the backend's own can be sent")
			}
		case "x-real-ip", "x-forwarded-for", "x-forwarded-proto", "x-forwarded-host", "x-forwarded-port", "forwarded":
			c.note(d, Ignored, "LOTUS sets "+name+" itself")
		case "connection", "upgrade":
			c.note(d, Ignored, "LOTUS forwards connection upgrades (WebSocket) by itself")
		case "keep-alive", "te", "trailer", "transfer-encoding", "content-length", "proxy-authorization", "proxy-connection":
			c.flag(o, d, Unsupported, name+" can't be set by proxy_set_header in LOTUS")
		default:
			if strings.Contains(val, "$") {
				c.flag(o, d, Unsupported, "header values with variables can't be translated; proxy_set_header takes fixed values")
				continue
			}
			if !headerNameRe.MatchString(name) {
				c.flag(o, d, Unsupported, fmt.Sprintf("%q is not a valid header name", name))
				continue
			}
			o.line("proxy_set_header %s %s", quote(name), quote(val))
			c.ok(d)
		}
	}
	if !hostSet {
		// Without proxy_set_header Host, Nginx sends the backend's own name.
		o.line("proxy_host upstream")
		c.note(pass, Translated, "Nginx sends the backend's own Host unless told otherwise, so proxy_host upstream was added")
	}
}

func (c *conv) fastcgiPass(o *out, sc *scope, d *Node) string {
	if len(d.Args) != 1 || strings.Contains(d.Args[0], "$") {
		c.flag(o, d, Unsupported, "fastcgi_pass with variables can't be translated")
		return ""
	}
	addr := d.Args[0]
	if u, ok := c.umap[addr]; ok && u.explicit {
		if len(u.backends) > 1 {
			c.flag(o, d, Approximated, "a FastCGI upstream with several servers is not balanced; the first server is used")
		}
		addr = u.backends[0].addr
		u.used = true
	}
	if !strings.HasPrefix(addr, "unix:") && !strings.Contains(addr, ":") {
		c.flag(o, d, Unsupported, fmt.Sprintf("%q is not an address: use host:port or unix:/path", addr))
		return ""
	}
	p, ok := c.pmap[addr]
	if !ok {
		p = &phpBackend{name: "php-" + strings.Trim(sanitizeRe.ReplaceAllString(addr, "-"), "-"), pass: addr}
		c.pmap[addr] = p
		c.php = append(c.php, p)
	}
	if t := sc.one("fastcgi_read_timeout"); t != nil && len(t.Args) == 1 {
		if v, ok := duration(t.Args[0]); ok {
			if p.timeout != "" && p.timeout != v {
				c.flag(nil, t, Approximated, "the timeout belongs to the PHP backend and is shared by its locations; the first value is kept")
			} else {
				p.timeout = v
				c.ok(t)
			}
		}
	}
	c.ok(d)
	return p.name
}

// ---- try_files, rewrite ----

func (c *conv) tryFiles(o *out, d *Node) []string {
	if len(d.Args) == 0 {
		return nil
	}
	var out []string
	for i, a := range d.Args {
		last := i == len(d.Args)-1
		switch {
		case last && strings.HasPrefix(a, "="):
			if n, err := strconv.Atoi(a[1:]); err != nil || n < 100 || n > 599 {
				c.flag(o, d, Unsupported, fmt.Sprintf("%q is not a status", a))
				return nil
			}
			out = append(out, quote(a))
		case strings.HasPrefix(a, "@"):
			name, ok := namedRoute(a)
			if !ok {
				c.flag(o, d, Unsupported, fmt.Sprintf("the name %q can't be used", a))
				return nil
			}
			out = append(out, quote(name))
		default:
			parts, bad := translate(a, ctxInternal)
			if len(bad) > 0 {
				c.flag(o, d, Unsupported, "no equivalent for "+strings.Join(bad, ", "))
				return nil
			}
			out = append(out, tmpl(parts))
		}
	}
	c.ok(d)
	return out
}

func (c *conv) rewrite(o *out, d *Node) string {
	if len(d.Args) < 2 || len(d.Args) > 3 {
		c.flag(o, d, Unsupported, "a rewrite needs a pattern and a replacement")
		return ""
	}
	pattern, repl := d.Args[0], d.Args[1]
	flagArg := ""
	if len(d.Args) == 3 {
		flagArg = d.Args[2]
	}
	if _, err := regexp.Compile(pattern); err != nil {
		c.flag(o, d, Unsupported, "the pattern is not valid in LOTUS (RE2: no backreferences or lookaround): "+strings.TrimPrefix(err.Error(), "error parsing regexp: "))
		return ""
	}
	pat, ok := raw(pattern)
	if !ok {
		c.flag(o, d, Unsupported, "a pattern with a single quote can't be written")
		return ""
	}
	parts, bad := translate(repl, ctxInternal)
	if len(bad) > 0 {
		c.flag(o, d, Unsupported, "no equivalent for "+strings.Join(bad, ", "))
		return ""
	}
	external := strings.HasPrefix(repl, "http://") || strings.HasPrefix(repl, "https://") || strings.HasPrefix(repl, "$scheme")
	lotusFlag := ""
	switch flagArg {
	case "":
		if external {
			lotusFlag = " redirect"
		}
	case "last":
	case "break":
		c.flag(o, d, Approximated, "break keeps the rewritten URI in this location; LOTUS routes the new URI again (last)")
	case "redirect":
		lotusFlag = " redirect"
	case "permanent":
		lotusFlag = " permanent"
	default:
		c.flag(o, d, Unsupported, fmt.Sprintf("the flag %q is not known", flagArg))
		return ""
	}
	if external && lotusFlag == "" {
		c.flag(o, d, Unsupported, "a rewrite to another site must redirect")
		return ""
	}
	if flagArg == "" || flagArg == "last" || flagArg == "redirect" || flagArg == "permanent" {
		c.ok(d)
	}
	return fmt.Sprintf("rewrite %s %s%s", pat, tmpl(parts), lotusFlag)
}

// ---- if, allow/deny ----

var methodEnum = map[string]bool{"GET": true, "HEAD": true, "POST": true, "PUT": true, "DELETE": true, "PATCH": true, "OPTIONS": true}

func (c *conv) ifMatch(o *out, d *Node) []string {
	fail := func(msg string) []string { c.flag(o, d, Unsupported, msg); return nil }
	if len(d.Block) != 1 || d.Block[0].Name != "return" {
		return fail("only an if with a single return inside can be translated (a match block)")
	}
	args := append([]string(nil), d.Args...)
	if len(args) == 0 {
		return fail("empty condition")
	}
	args[0] = strings.TrimPrefix(args[0], "(")
	args[len(args)-1] = strings.TrimSuffix(args[len(args)-1], ")")
	if args[0] == "" {
		args = args[1:]
	}
	if n := len(args); n > 0 && args[n-1] == "" {
		args = args[:n-1]
	}
	if len(args) != 3 {
		return fail("only conditions of the form $variable operator value can be translated")
	}
	v, op, val := args[0], args[1], args[2]
	ret, ok := c.returnLines(d.Block[0])
	if !ok {
		return fail("the return inside can't be translated")
	}
	var cond string
	switch {
	case v == "$request_method" && op == "=":
		if !methodEnum[val] {
			return fail(fmt.Sprintf("%q is not a method LOTUS can match", val))
		}
		cond = fmt.Sprintf("method [ %s ]", quote(val))
	case v == "$scheme" && op == "=" && (val == "http" || val == "https"):
		cond = "scheme " + quote(val)
	case (v == "$host" || v == "$http_host") && op == "=" && hostnameRe.MatchString(strings.ToLower(val)):
		cond = "host [ " + quote(strings.ToLower(val)) + " ]"
	case v == "$remote_addr" && (op == "=" || op == "!="):
		cidr, ok := toCIDR(val)
		if !ok {
			return fail(fmt.Sprintf("%q is not an address or network", val))
		}
		if op == "=" {
			cond = fmt.Sprintf("client_ip [ %s ]", quote(cidr))
		} else {
			cond = fmt.Sprintf("client_ip !~ [ %s ]", quote(cidr))
		}
	default:
		kind, name := "", ""
		switch {
		case strings.HasPrefix(v, "$http_") && len(v) > 6:
			kind, name = "header", headerName(v[6:])
		case strings.HasPrefix(v, "$arg_") && len(v) > 5:
			kind, name = "query", v[5:]
		case strings.HasPrefix(v, "$cookie_") && len(v) > 8:
			kind, name = "cookie", v[8:]
		}
		if kind == "" {
			return fail("the condition on " + v + " has no equivalent")
		}
		switch op {
		case "=", "~", "~*", "!~", "!~*":
		case "!=":
			return fail("!= has no equivalent; use !~ with an anchored pattern")
		default:
			return fail("the operator " + op + " is not known")
		}
		if op != "=" {
			if _, err := regexp.Compile(map[bool]string{true: "(?i)", false: ""}[strings.HasSuffix(op, "*")] + val); err != nil {
				return fail("the pattern is not valid in LOTUS (RE2)")
			}
		}
		r, valid := raw(val)
		if !valid {
			return fail("a value with a single quote can't be written")
		}
		cond = fmt.Sprintf("%s %s %s %s", kind, quote(name), op, r)
	}
	c.ok(d)
	return append([]string{cond}, ret...)
}

func toCIDR(v string) (string, bool) {
	if strings.Contains(v, "/") {
		_, _, err := net.ParseCIDR(v)
		return v, err == nil
	}
	ip := net.ParseIP(v)
	if ip == nil {
		return "", false
	}
	if ip.To4() != nil {
		return v + "/32", true
	}
	return v + "/128", true
}

// accessRules translates allow/deny lists. It returns a match block, or
// denyAll when the location denies everybody.
func (c *conv) accessRules(o *out, seq []*Node) (match []string, denyAll bool) {
	// Order matters in Nginx, but the two shapes people write are all-allows
	// followed by deny all, and all-denies followed by allow all.
	if len(seq) == 0 {
		return nil, false
	}
	last := seq[len(seq)-1]
	nets := func(kind string) ([]string, bool) {
		var out []string
		for _, d := range seq[:len(seq)-1] {
			if d.Name != kind || len(d.Args) != 1 {
				return nil, false
			}
			if d.Args[0] == "unix:" {
				continue
			}
			cidr, ok := toCIDR(d.Args[0])
			if !ok {
				return nil, false
			}
			out = append(out, quote(cidr))
		}
		return out, true
	}
	all := func(d *Node, kind string) bool {
		return d.Name == kind && len(d.Args) == 1 && d.Args[0] == "all"
	}
	switch {
	case len(seq) == 1 && all(last, "deny"):
		c.ok(last)
		return nil, true
	case len(seq) == 1 && all(last, "allow"):
		c.note(last, Ignored, "allow all is what LOTUS does without rules")
		return nil, false
	case all(last, "deny"):
		if list, ok := nets("allow"); ok && len(list) > 0 {
			for _, d := range seq {
				c.ok(d)
			}
			return []string{fmt.Sprintf("client_ip !~ [ %s ]", strings.Join(list, ", ")), "return 403"}, false
		}
	case all(last, "allow"):
		if list, ok := nets("deny"); ok && len(list) > 0 {
			for _, d := range seq {
				c.ok(d)
			}
			return []string{fmt.Sprintf("client_ip = [ %s ]", strings.Join(list, ", ")), "return 403"}, false
		}
	}
	for _, d := range seq {
		c.flag(o, d, Unsupported, "only \"allow ...; deny all;\" and \"deny ...; allow all;\" can be translated")
	}
	return nil, false
}

// ---- cache ----

func (c *conv) routeCache(o *out, sc *scope, d *Node, kind string, hasProxy, hasPHP bool) {
	if len(d.Args) != 1 || d.Args[0] == "off" {
		c.ok(d)
		return
	}
	zone := d.Args[0]
	if strings.Contains(zone, "$") {
		c.flag(o, d, Unsupported, "a cache zone chosen by a variable can't be translated")
		return
	}
	if (kind == "proxy" && !hasProxy) || (kind == "fastcgi" && !hasPHP) {
		c.flag(o, d, Unsupported, "only the responses of a proxy or PHP are cached, and this location has neither")
		return
	}
	if _, ok := c.cmap[zone]; !ok {
		c.flag(o, d, Unsupported, fmt.Sprintf("the zone %q was not defined by proxy_cache_path or fastcgi_cache_path", zone))
		return
	}
	var lines []string
	for _, v := range sc.find(kind + "_cache_valid") {
		if len(v.Args) < 1 {
			continue
		}
		t, ok := duration(v.Args[len(v.Args)-1])
		if !ok {
			c.flag(o, v, Unsupported, "not a time")
			continue
		}
		codes := v.Args[:len(v.Args)-1]
		pos, neg := len(codes) == 0, false
		for _, cd := range codes {
			switch cd {
			case "200", "any":
				pos = true
			case "404", "410":
				neg = true
			}
		}
		switch {
		case pos && !hasLine(lines, "ttl "):
			lines = append(lines, "ttl "+t)
			c.ok(v)
		case neg && !hasLine(lines, "negative_ttl "):
			lines = append(lines, "negative_ttl "+t)
			c.ok(v)
		case pos || neg:
			c.ok(v)
		default:
			c.flag(o, v, Approximated, "only the lifetime of 200 responses (ttl) and of 404 and 410 (negative_ttl) can be set; the response's own headers decide the rest")
		}
	}
	if u := sc.one(kind + "_cache_use_stale"); u != nil {
		stale := false
		for _, a := range u.Args {
			if a == "error" || a == "timeout" || a == "http_500" || a == "http_502" || a == "http_503" || a == "http_504" {
				stale = true
			}
		}
		if stale {
			lines = append(lines, "stale_if_error 1h")
			c.flag(o, u, Approximated, "Nginx serves stale entries without a time limit; LOTUS serves them for up to an hour past their lifetime (stale_if_error)")
		} else {
			c.note(u, Ignored, "LOTUS refills an expired entry with one request while the others wait (lock_timeout)")
		}
	}
	if l := sc.one(kind + "_cache_lock_timeout"); l != nil && len(l.Args) == 1 {
		if t, ok := duration(l.Args[0]); ok {
			lines = append(lines, "lock_timeout "+t)
			c.ok(l)
		}
	}
	if k := sc.one(kind + "_cache_key"); k != nil {
		key := strings.Join(k.Args, " ")
		switch key {
		case "$scheme$proxy_host$request_uri", "$scheme$host$request_uri", "$scheme$proxy_host$uri$is_args$args", "$scheme$host$request_uri$is_args":
			c.note(k, Translated, "the key is scheme, host, path and query, as in LOTUS")
		default:
			c.flag(o, k, Unsupported, "a custom cache key is not supported; LOTUS uses scheme, host, path and query")
		}
	}
	for _, n := range []string{kind + "_cache_bypass", kind + "_no_cache"} {
		if b := sc.one(n); b != nil {
			c.flag(o, b, Unsupported, "LOTUS skips the cache for requests with cookies (see bypass_cookies) and never keeps responses that set cookies or say private or no-store; other conditions can't be set")
		}
	}
	if len(lines) == 0 {
		o.line("cache %s", quote(zone))
	} else {
		o.open("cache %s", quote(zone))
		for _, l := range lines {
			o.line("%s", l)
		}
		o.close()
	}
	c.ok(d)
}

func hasLine(lines []string, prefix string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}
