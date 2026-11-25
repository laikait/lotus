package nginxconv

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type listenSpec struct {
	host  string // "0.0.0.0", "[::]" or a name/address
	port  string
	ssl   bool
	def   bool
	quic  bool
	node  *Node
	any   bool // the wildcard address of its family
	v6any bool
}

// parseListen reads `listen` arguments.
func (c *conv) parseListen(n *Node) (listenSpec, bool) {
	ls := listenSpec{node: n}
	if len(n.Args) == 0 {
		c.flag(nil, n, Unsupported, "listen needs an address")
		return ls, false
	}
	a := n.Args[0]
	if strings.HasPrefix(a, "unix:") {
		c.flag(nil, n, Unsupported, "listening on a unix socket is not supported")
		return ls, false
	}
	host, port := "", ""
	switch {
	case strings.HasPrefix(a, "["):
		end := strings.Index(a, "]")
		if end < 0 {
			c.flag(nil, n, Unsupported, "malformed address")
			return ls, false
		}
		host = a[:end+1]
		port = strings.TrimPrefix(a[end+1:], ":")
	case strings.Contains(a, ":"):
		h, p, err := net.SplitHostPort(a)
		if err != nil {
			c.flag(nil, n, Unsupported, "malformed address")
			return ls, false
		}
		host, port = h, p
	case isDigits(a):
		port = a
	default:
		host = a
	}
	if port == "" {
		port = "80"
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		c.flag(nil, n, Unsupported, fmt.Sprintf("%q is not a port", port))
		return ls, false
	}
	switch host {
	case "", "*", "0.0.0.0":
		host, ls.any = "0.0.0.0", true
	case "[::]":
		ls.any, ls.v6any = true, true
	}
	ls.host, ls.port = host, port
	for _, f := range n.Args[1:] {
		switch {
		case f == "ssl":
			ls.ssl = true
		case f == "quic":
			ls.quic, ls.ssl = true, true
		case f == "default_server" || f == "default":
			ls.def = true
		case f == "http2" || f == "reuseport" || f == "deferred" || f == "bind" || strings.HasPrefix(f, "ipv6only=") ||
			strings.HasPrefix(f, "backlog=") || strings.HasPrefix(f, "so_keepalive=") || strings.HasPrefix(f, "rcvbuf=") ||
			strings.HasPrefix(f, "sndbuf=") || strings.HasPrefix(f, "fastopen=") || strings.HasPrefix(f, "setfib="):
			// socket tuning and protocol switches LOTUS decides itself
		case f == "proxy_protocol":
			c.flag(nil, n, Unsupported, "PROXY protocol is not implemented yet")
		default:
			c.flag(nil, n, Approximated, fmt.Sprintf("the listen parameter %q was dropped", f))
		}
	}
	return ls, true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// listenerFor returns the LOTUS listener for an address, creating it.
func (c *conv) listenerFor(ls listenSpec) *listener {
	key := ls.host + ":" + ls.port + "/" + strconv.FormatBool(ls.ssl)
	if ls.any {
		key = "any:" + ls.port + "/" + strconv.FormatBool(ls.ssl)
	}
	if l, ok := c.lmap[key]; ok {
		if ls.any && ls.v6any && l.addr != "[::]:"+ls.port {
			// An IPv4 and an IPv6 wildcard of the same port become one
			// dual-stack listener.
			l.addr = "[::]:" + ls.port
			c.flag(nil, ls.node, Approximated, "the IPv4 and IPv6 listeners of port "+ls.port+" became one dual-stack listener, [::]:"+ls.port)
		}
		return l
	}
	proto := "http"
	if ls.ssl {
		proto = "https"
	}
	name := proto + "-" + ls.port
	for taken := true; taken; {
		taken = false
		for _, l := range c.listeners {
			if l.name == name {
				taken = true
				name += "-" + strconv.Itoa(len(c.listeners)+1)
				break
			}
		}
	}
	l := &listener{name: name, addr: ls.host + ":" + ls.port, https: ls.ssl}
	c.lmap[key] = l
	c.listeners = append(c.listeners, l)
	return l
}

var hostnameRe = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]*[a-z0-9])?)(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

type redirect struct {
	status int
	target string // a LOTUS string literal, placeholders included
}

// srv is a server block on its way to becoming a site.
type srv struct {
	node      *Node
	sc        *scope
	names     []string
	catchAll  bool
	listens   []listenSpec
	redirect  *redirect // set when the server only redirects to https
	redirects []*redirect
	extra     []*listener // listeners of merged redirect servers
	merged    bool
	def       bool
}

func (c *conv) server(n *Node, parent *scope) {
	s := &srv{node: n, sc: parent.child(n.Block)}
	c.srvs = append(c.srvs, s)
	c.serverHead(s)
}

func (c *conv) serverHead(s *srv) {
	n, sc := s.node, s.sc
	for _, d := range sc.own("listen") {
		if ls, ok := c.parseListen(d); ok {
			s.listens = append(s.listens, ls)
			if ls.def {
				s.def = true
			}
		}
	}
	if len(sc.own("listen")) == 0 {
		s.listens = append(s.listens, listenSpec{host: "0.0.0.0", port: "80", any: true, node: n})
		c.flag(nil, n, Approximated, "no listen directive: Nginx listens on port 80 by default, so this server does")
	}
	seen := map[string]bool{}
	var names []string
	add := func(h string) {
		if !seen[h] {
			seen[h] = true
			names = append(names, h)
		}
	}
	for _, d := range sc.own("server_name") {
		for _, a := range d.Args {
			low := strings.ToLower(a)
			switch {
			case a == "_" || a == "":
				s.catchAll = true
			case strings.HasPrefix(a, "~"):
				c.flag(nil, d, Unsupported, "a regular expression server name has no equivalent; list the names")
			case strings.HasPrefix(low, "."):
				base := strings.TrimPrefix(low, ".")
				if hostnameRe.MatchString(base) {
					add(base)
					add("*." + base)
				}
			case hostnameRe.MatchString(low):
				add(low)
			default:
				c.flag(nil, d, Unsupported, fmt.Sprintf("the server name %q can't be used (LOTUS accepts names and *.names)", a))
			}
		}
		c.ok(d)
	}
	s.names = names
	if len(names) == 0 {
		s.catchAll = true
	}
	s.redirect = c.pureRedirect(s)
}

// pureRedirect recognises a server that does nothing but send everyone to
// https: the usual port 80 companion of an https server.
func (c *conv) pureRedirect(s *srv) *redirect {
	var ret *Node
	for _, d := range s.node.Block {
		switch d.Name {
		case "listen", "server_name", "access_log", "error_log", "return", "include":
			if d.Name == "return" {
				ret = d
			}
		case "location":
			if r := c.locationOnlyRedirect(d); r != nil {
				if ret != nil {
					return nil
				}
				ret = r
				continue
			}
			if isACME(d) {
				continue
			}
			return nil
		default:
			return nil
		}
	}
	if ret == nil || len(ret.Args) != 2 {
		return nil
	}
	code, err := strconv.Atoi(ret.Args[0])
	if err != nil || code < 300 || code > 399 || !strings.HasPrefix(ret.Args[1], "https://") {
		return nil
	}
	parts, bad := translate(ret.Args[1], ctxRequest)
	if len(bad) > 0 {
		return nil
	}
	return &redirect{status: code, target: tmpl(parts)}
}

func (c *conv) locationOnlyRedirect(d *Node) *Node {
	if len(d.Args) != 1 || d.Args[0] != "/" || len(d.Block) != 1 || d.Block[0].Name != "return" {
		return nil
	}
	return d.Block[0]
}

func isACME(d *Node) bool {
	for _, a := range d.Args {
		if strings.Contains(a, ".well-known/acme-challenge") {
			return true
		}
	}
	return false
}

func sameNames(a, b []string) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// buildSites pairs redirect-only servers with the https server of the same
// names (LOTUS has one site per name), then writes every site.
func (c *conv) buildSites() {
	for _, r := range c.srvs {
		if r.redirect == nil {
			continue
		}
		for _, t := range c.srvs {
			if t == r || t.redirect != nil || t.merged || !sameNames(t.names, r.names) || !hasSSL(t) || hasSSL(r) {
				continue
			}
			t.redirects = append(t.redirects, r.redirect)
			for _, ls := range r.listens {
				t.extra = append(t.extra, c.listenerFor(ls))
			}
			r.merged = true
			c.noteMerge(r, t)
			break
		}
	}
	taken := map[string]*srv{}
	for _, s := range c.srvs {
		if s.merged {
			continue
		}
		if len(s.listens) == 0 {
			c.flag(nil, s.node, Unsupported, "none of the listen directives of this server can be translated, so the server was left out")
			continue
		}
		name := c.siteName(s)
		if other, dup := taken[name]; dup {
			c.flag(nil, s.node, Unsupported, fmt.Sprintf("the server name %q is used by the server at line %d too; LOTUS has one site per name, so this one was left out", name, other.node.Line))
			continue
		}
		taken[name] = s
		// Every other name must be free too: a host name belongs to one site.
		kept := s.names[:0:0]
		for i, h := range s.names {
			if i > 0 {
				if other, dup := taken[h]; dup {
					c.flag(nil, s.node, Approximated, fmt.Sprintf("the name %q is served by the site of line %d already; it was dropped from this one", h, other.node.Line))
					continue
				}
				taken[h] = s
			}
			kept = append(kept, h)
		}
		s.names = kept
		c.sites = append(c.sites, c.writeSite(s, name))
	}
}

func hasSSL(s *srv) bool {
	for _, l := range s.listens {
		if l.ssl {
			return true
		}
	}
	return false
}

func (c *conv) noteMerge(r, t *srv) {
	c.note(r.node, Translated, fmt.Sprintf("this server only redirects to https; it was merged into the site of line %d as a redirect for plain-http requests", t.node.Line))
	// the merge is reported at the https site, where the routes got it
}

func (c *conv) siteName(s *srv) string {
	if len(s.names) > 0 {
		return s.names[0]
	}
	return "default.invalid"
}

func (c *conv) writeSite(s *srv, name string) *site {
	var o out
	o.ind = 1
	n, sc := s.node, s.sc
	st := &site{name: name, def: s.def, catchAll: s.catchAll, line: n.Line}

	if len(s.names) == 0 {
		c.flag(&o, n, Approximated, `this server has no real host name (server_name _ or none): it is called "default.invalid" and answers requests for unknown hosts only while it is the first site of a listener`)
		st.def = true
	}
	if s.catchAll && len(s.names) > 0 {
		c.note(n, Ignored, "the catch-all name _ is left out; a site answers unknown hosts when it is first on its listener")
	}

	// listeners
	var names []string
	seen := map[string]bool{}
	addL := func(l *listener) {
		if !seen[l.name] {
			seen[l.name] = true
			names = append(names, l.name)
		}
	}
	for _, ls := range s.listens {
		if ls.quic {
			c.quicPorts(ls)
		}
		l := c.listenerFor(ls)
		addL(l)
	}
	for _, l := range s.extra {
		addL(l)
	}
	st.listeners = names

	for _, r := range s.redirects {
		o.comment(fmt.Sprintf("Plain-http requests are redirected by the match blocks below: they replace the Nginx server that only redirected (status %d).", r.status))
	}
	o.open("site %s", quote(name))
	o.line("listeners [ %s ]", joinQuoted(names))
	if len(s.names) > 1 {
		o.line("aliases [ %s ]", joinQuoted(s.names[1:]))
	}

	c.tlsOfSite(&o, s)

	// root, index, error pages
	root := ""
	if d := sc.one("root"); d != nil && len(d.Args) == 1 {
		if strings.Contains(d.Args[0], "$") {
			c.flag(&o, d, Unsupported, "a document root with variables can't be translated")
		} else {
			root = d.Args[0]
			o.line("root %s", quote(root))
			c.ok(d)
		}
	}
	if d := sc.one("index"); d != nil {
		if files := c.indexFiles(d); len(files) > 0 {
			o.line("index [ %s ]", joinQuoted(files))
			c.ok(d)
		}
	}
	c.errorPages(&o, sc.find("error_page"))

	c.siteLogs(&o, sc)

	// limits, compression, headers, rate limit
	if d := sc.one("client_max_body_size"); d != nil {
		if _, own := ownHas(sc, "client_max_body_size"); own {
			if v, ok := c.bodyLimit(d); ok {
				o.open("limits")
				o.line("max_request_body %s", v)
				o.close()
			}
		}
	}
	if cfg := c.compression(sc, c.httpComp); !cfg.equal(c.httpComp) {
		switch {
		case cfg == nil:
			o.open("compression")
			o.line("enabled false")
			o.close()
		default:
			cfg.write(&o)
		}
	}
	c.securityHeaders(&o, sc)
	if d := sc.one("limit_req"); d != nil {
		c.rateLimit(&o, sc, d)
	}

	rc := &routeCtx{c: c, root: root, redirects: s.redirects}
	topSeen := map[string]bool{}
	hasRoot := false
	serverReturned := false
	for _, d := range n.Block {
		if d.Name == "return" {
			if _, ok := c.returnLines(d); ok {
				serverReturned = true
			}
		}
	}
	for _, d := range n.Block {
		switch d.Name {
		case "listen", "server_name", "root", "index", "error_page", "client_max_body_size", "limit_req", "gzip", "gzip_types",
			"gzip_min_length", "gzip_comp_level", "gzip_static", "add_header", "access_log", "error_log", "ssl_certificate",
			"ssl_certificate_key":
			// handled above
		case "location":
			if serverReturned {
				c.note(d, Ignored, "the return of the server answers every request before locations are looked at, so this location never runs")
				continue
			}
			if c.route(&o, sc, d, nil, rc, topSeen) && routeIsRoot(d) {
				hasRoot = true
			}
		case "return":
			c.serverReturn(&o, sc, d, rc, &hasRoot)
		case "rewrite":
			c.flag(&o, d, Unsupported, "a rewrite outside a location is not supported yet; put it in a location")
		case "if":
			c.flag(&o, d, Unsupported, "an if outside a location is not supported; LOTUS has match blocks inside routes")
		case "include":
			c.includeNote(d)
		case "ssl_protocols":
			c.sslProtocols(d)
		case "keepalive_timeout", "client_header_timeout":
			c.flag(&o, d, Approximated, "this setting is server-wide in LOTUS (limits at the top of the server block), so it was not applied to this site")
		case "allow", "deny", "auth_basic", "auth_basic_user_file":
			c.flag(&o, d, Unsupported, "access rules outside a location can't be translated; put them in a location")
		default:
			c.contextDirective(&o, sc, d, "server")
		}
	}
	if len(s.redirects) > 0 && !hasRoot {
		o.open(`route "/"`)
		c.injectRedirects(&o, rc)
		o.close()
	}
	o.close()
	st.body = o.b.String()
	return st
}

func ownHas(sc *scope, name string) (*Node, bool) {
	l := sc.own(name)
	if len(l) == 0 {
		return nil, false
	}
	return l[0], true
}

func routeIsRoot(d *Node) bool {
	if len(d.Args) == 1 {
		return d.Args[0] == "/"
	}
	return len(d.Args) == 2 && d.Args[0] == "^~" && d.Args[1] == "/"
}

func (c *conv) indexFiles(d *Node) []string {
	var files []string
	for _, a := range d.Args {
		if strings.Contains(a, "$") {
			c.flag(nil, d, Approximated, "index entries with variables were dropped")
			continue
		}
		files = append(files, a)
	}
	return files
}

// quicPorts turns `listen 443 quic` into http3 on the https listener.
func (c *conv) quicPorts(ls listenSpec) {
	c.quic = append(c.quic, ls)
}

func (c *conv) finishQuic() {
	for _, ls := range c.quic {
		found := false
		for _, l := range c.listeners {
			if l.https && strings.HasSuffix(l.addr, ":"+ls.port) {
				l.http3, found = true, true
			}
		}
		if found {
			c.note(ls.node, Translated, "HTTP/3 is on for the https listener of that port")
		} else {
			c.flag(nil, ls.node, Unsupported, "no https listener on that port to switch HTTP/3 on for")
		}
	}
}

func (c *conv) tlsOfSite(o *out, s *srv) {
	sc := s.sc
	if !hasSSL(s) {
		o.line("tls off")
		return
	}
	cert, key := sc.one("ssl_certificate"), sc.one("ssl_certificate_key")
	switch {
	case cert == nil || key == nil:
		c.placeholderCert(o, s.node, "the server listens with ssl but names no certificate")
	case len(cert.Args) != 1 || len(key.Args) != 1 || strings.Contains(cert.Args[0]+key.Args[0], "$") || strings.HasPrefix(key.Args[0], "engine:"):
		c.placeholderCert(o, cert, "certificate files that are variables or hardware keys can't be translated")
	default:
		o.line("tls manual")
		o.line("certificate %s", quote(cert.Args[0]))
		o.line("certificate_key %s", quote(key.Args[0]))
		c.ok(cert)
		c.ok(key)
		if len(sc.find("ssl_certificate")) > 1 {
			c.flag(o, sc.find("ssl_certificate")[1], Approximated, "a site has one certificate; the first one is used")
		}
	}
}

// placeholderCert writes certificate paths to be filled in: the site then
// fails to start with a clear message instead of serving without TLS. (LOTUS
// can also obtain certificates itself; see tls { acme { ... } }.)
func (c *conv) placeholderCert(o *out, n *Node, why string) {
	o.line("tls manual")
	o.line(`certificate "/etc/lotus/certs/CHANGE-ME-fullchain.pem"`)
	o.line(`certificate_key "/etc/lotus/certs/CHANGE-ME-privkey.pem"`)
	c.flag(o, n, Unsupported, why+": set the two certificate paths, or use an acme block in tls and tls automatic")
}

func (c *conv) siteLogs(o *out, sc *scope) {
	var access, errorLog string
	accessSet := false
	for _, d := range sc.find("access_log") {
		if len(d.Args) == 0 || accessSet {
			continue
		}
		switch {
		case d.Args[0] == "off":
			access, accessSet = "null", true
			c.ok(d)
		case strings.HasPrefix(d.Args[0], "syslog:"):
			c.flag(nil, d, Unsupported, "logging to syslog is not supported")
		case strings.Contains(d.Args[0], "$"):
			c.flag(nil, d, Unsupported, "log file names with variables can't be translated")
		default:
			access, accessSet = quote(d.Args[0]), true
			if len(d.Args) > 1 && d.Args[1] != "combined" {
				c.note(d, Ignored, "LOTUS writes its own access-log format (no log_format)")
			} else {
				c.ok(d)
			}
		}
	}
	if d := sc.one("error_log"); d != nil && len(d.Args) >= 1 {
		switch {
		case d.Args[0] == "stderr" || strings.HasPrefix(d.Args[0], "syslog:") || strings.HasPrefix(d.Args[0], "memory:"):
			c.note(d, Ignored, "LOTUS writes errors to its server log")
		case strings.Contains(d.Args[0], "$"):
			c.flag(nil, d, Unsupported, "log file names with variables can't be translated")
		default:
			errorLog = quote(d.Args[0])
			c.note(d, Translated, "the log level is not translated")
		}
	}
	if access == "" && errorLog == "" {
		return
	}
	o.open("logs")
	if access != "" {
		o.line("access %s", access)
	}
	if errorLog != "" {
		o.line("error %s", errorLog)
	}
	o.close()
}

func (c *conv) securityHeaders(o *out, sc *scope) {
	heads := sc.find("add_header")
	if len(heads) == 0 {
		return
	}
	var lines []string
	seen := map[string]bool{}
	for _, d := range heads {
		if len(d.Args) < 2 {
			continue
		}
		name, val := d.Args[0], d.Args[1]
		switch {
		case !headerNameRe.MatchString(name):
			c.flag(nil, d, Unsupported, fmt.Sprintf("%q is not a valid header name", name))
		case strings.Contains(val, "$"):
			c.flag(nil, d, Unsupported, "header values with variables can't be translated")
		case seen[strings.ToLower(name)]:
			c.flag(nil, d, Unsupported, "this header was already added; LOTUS sets each header once")
		default:
			seen[strings.ToLower(name)] = true
			lines = append(lines, fmt.Sprintf("%s = %s", quote(name), quote(val)))
			c.ok(d)
		}
	}
	if len(lines) == 0 {
		return
	}
	o.open("security_headers")
	for _, l := range lines {
		o.line("%s", l)
	}
	o.close()
}

var headerNameRe = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

func (c *conv) errorPages(o *out, nodes []*Node) {
	seen := map[string]bool{}
	for _, d := range nodes {
		if len(d.Args) < 2 {
			continue
		}
		target := d.Args[len(d.Args)-1]
		var codes []string
		bad := ""
		for _, a := range d.Args[:len(d.Args)-1] {
			if strings.HasPrefix(a, "=") {
				bad = "changing the status of the error page (error_page ... =200) is not supported"
				continue
			}
			n, err := strconv.Atoi(a)
			if err != nil || n < 400 || n > 599 {
				bad = fmt.Sprintf("only statuses 400 to 599 can have an error page (%s)", a)
				continue
			}
			codes = append(codes, a)
		}
		if strings.Contains(target, "$") {
			bad = "an error page with variables can't be translated"
		}
		switch {
		case strings.HasPrefix(target, "@"):
			t, ok := namedRoute(target)
			if !ok {
				bad = fmt.Sprintf("the name %q can't be used", target)
			}
			target = t
		case strings.HasPrefix(target, "/"):
		default:
			bad = "an error page that redirects to a URL is not supported; use a path or a named route"
		}
		if bad != "" {
			c.flag(o, d, Unsupported, bad)
			continue
		}
		for _, code := range codes {
			if seen[code] {
				c.flag(o, d, Unsupported, "the status "+code+" already has an error page here; LOTUS has one per status")
				continue
			}
			seen[code] = true
			o.line("error_page %s %s", code, quote(target))
		}
		c.ok(d)
	}
}

var namedRe = regexp.MustCompile(`^@[a-z0-9_-]+$`)

func namedRoute(s string) (string, bool) {
	low := strings.ToLower(s)
	return low, namedRe.MatchString(low)
}

func (c *conv) serverReturn(o *out, sc *scope, d *Node, rc *routeCtx, hasRoot *bool) {
	if d.Args == nil {
		return
	}
	lines, ok := c.returnLines(d)
	if !ok {
		c.flag(o, d, Unsupported, "this return can't be translated")
		return
	}
	if !*hasRoot {
		*hasRoot = true
		o.open(`route "/"`)
		for _, l := range lines {
			o.line("%s", l)
		}
		o.close()
	}
	c.ok(d)
}

// rateLimit writes limit_req as a rate_limit block.
func (c *conv) rateLimit(o *out, sc *scope, d *Node) {
	var zone string
	burst := 0
	nodelay := false
	for _, a := range d.Args {
		switch {
		case strings.HasPrefix(a, "zone="):
			zone = strings.TrimPrefix(a, "zone=")
		case strings.HasPrefix(a, "burst="):
			burst, _ = strconv.Atoi(strings.TrimPrefix(a, "burst="))
		case a == "nodelay":
			nodelay = true
		case strings.HasPrefix(a, "delay="):
			nodelay = false
		}
	}
	z := c.zones[zone]
	if z == nil || !z.ok {
		c.flag(o, d, Unsupported, fmt.Sprintf("the zone %q was not defined by a usable limit_req_zone", zone))
		return
	}
	o.open("rate_limit")
	o.line("requests %d", z.requests)
	o.line("per %s", z.per)
	o.line("burst %d", burst+1)
	o.close()
	status := sc.one("limit_req_status")
	if burst > 0 && !nodelay || status == nil || len(status.Args) != 1 || status.Args[0] != "429" {
		c.flag(o, d, Approximated, "LOTUS refuses requests over the limit with 429 at once (Nginx answers 503 unless limit_req_status is set, and delays bursts unless nodelay is given); the burst here is Nginx's burst plus one")
	} else {
		c.ok(d)
	}
}
