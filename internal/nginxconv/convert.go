package nginxconv

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Status says how a directive was translated.
type Status string

const (
	// Translated: the same behavior in LOTUS.
	Translated Status = "translated"
	// Approximated: LOTUS does the nearest thing, and it is not exactly what
	// Nginx did. Review it.
	Approximated Status = "approximated"
	// Ignored: nothing to translate; LOTUS needs no such setting.
	Ignored Status = "ignored"
	// Unsupported: the behavior is lost. It is left in the output as a comment.
	Unsupported Status = "unsupported"
)

// Issue is the outcome for one directive.
type Issue struct {
	File      string `json:"file"`
	Line      int    `json:"line"`
	Directive string `json:"directive"`
	Status    Status `json:"status"`
	Message   string `json:"message,omitempty"`
}

// Result is a translated configuration.
type Result struct {
	// Config is the LOTUS configuration text.
	Config string
	// Issues lists every directive that was not translated exactly, plus (with
	// All) the ones that were.
	Issues []Issue
}

// Count returns how many issues have the status.
func (r *Result) Count(s Status) int {
	n := 0
	for _, i := range r.Issues {
		if i.Status == s {
			n++
		}
	}
	return n
}

// NeedsReview reports whether anything was approximated or lost.
func (r *Result) NeedsReview() bool {
	return r.Count(Approximated)+r.Count(Unsupported) > 0
}

// scope is a configuration context with the one around it, because Nginx
// directives are inherited: a location sees what its server and http set.
type scope struct {
	parent *scope
	nodes  []*Node
}

func (s *scope) child(nodes []*Node) *scope { return &scope{parent: s, nodes: nodes} }

// find returns the directives of that name in the nearest context that has any.
func (s *scope) find(name string) []*Node {
	for ; s != nil; s = s.parent {
		var out []*Node
		for _, n := range s.nodes {
			if n.Name == name {
				out = append(out, n)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

func (s *scope) one(name string) *Node {
	if l := s.find(name); len(l) > 0 {
		return l[0]
	}
	return nil
}

// own returns the directives of that name in this context only.
func (s *scope) own(name string) []*Node {
	var out []*Node
	for _, n := range s.nodes {
		if n.Name == name {
			out = append(out, n)
		}
	}
	return out
}

type listener struct {
	name  string
	addr  string
	https bool
	http3 bool
}

type backend struct {
	addr     string
	weight   int
	maxConns int
}

type upstream struct {
	name        string
	backends    []backend
	balance     string
	connect     string
	response    string
	idle        string
	maxFails    string
	failTimeout string
	https       bool
	used        bool
	explicit    bool // defined by an upstream block
	line        int
	file        string
}

type phpBackend struct {
	name    string
	pass    string
	timeout string
}

type cacheZone struct {
	name    string
	maxSize string
}

type limitZone struct {
	requests int
	per      string
	ok       bool
}

type site struct {
	name      string
	listeners []string
	def       bool
	catchAll  bool
	body      string
	line      int
}

type conv struct {
	issues    []Issue
	seen      map[string]bool
	listeners []*listener
	lmap      map[string]*listener
	ups       []*upstream
	umap      map[string]*upstream
	php       []*phpBackend
	pmap      map[string]*phpBackend
	caches    []*cacheZone
	cmap      map[string]*cacheZone
	zones     map[string]*limitZone
	sites     []*site
	trusted   []string
	tsSeen    map[string]bool
	tlsMin    string
	tlsLine   *Node
	http      *scope
	httpComp  *compCfg
	httpBody  string
	httpLimit [][2]string
	all       bool
	srvs      []*srv
	quic      []listenSpec
}

// Convert translates parsed Nginx configuration. With all, directives that
// translated exactly are listed too.
func Convert(nodes []*Node, all bool) *Result {
	c := &conv{
		seen: map[string]bool{}, lmap: map[string]*listener{}, umap: map[string]*upstream{},
		pmap: map[string]*phpBackend{}, cmap: map[string]*cacheZone{}, zones: map[string]*limitZone{},
		tsSeen: map[string]bool{}, all: all,
	}
	c.top(nodes)
	cfg := c.render()
	return &Result{Config: cfg, Issues: c.issues}
}

// note records the outcome for a directive (once per file, line and message).
func (c *conv) note(n *Node, st Status, msg string) {
	if st == Translated && !c.all {
		return
	}
	key := fmt.Sprintf("%s:%d:%s:%s", n.File, n.Line, n.Name, msg)
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	c.issues = append(c.issues, Issue{File: n.File, Line: n.Line, Directive: n.Name, Status: st, Message: msg})
}

func (c *conv) ok(n *Node) { c.note(n, Translated, "") }

// text is a directive as it was written, for comments.
func text(n *Node) string {
	s := n.Name
	if len(n.Args) > 0 {
		s += " " + strings.Join(n.Args, " ")
	}
	if n.IsCtx {
		return s + " { ... }"
	}
	return s + ";"
}

// flag records a problem and, for the ones that change behavior, leaves a
// comment in the output where the directive would have been.
func (c *conv) flag(o *out, n *Node, st Status, msg string) {
	c.note(n, st, msg)
	if o != nil && (st == Unsupported || st == Approximated) {
		o.comment(fmt.Sprintf("nginx line %d, %s: %s", n.Line, st, msg))
		o.comment("    " + oneLine(text(n)))
	}
}

func oneLine(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
	if len(s) > 160 {
		s = s[:157] + "..."
	}
	return s
}

// ---- layout of the output ----

type out struct {
	b   strings.Builder
	ind int
}

func (o *out) line(format string, args ...any) {
	o.b.WriteString(strings.Repeat("    ", o.ind))
	fmt.Fprintf(&o.b, format, args...)
	o.b.WriteByte('\n')
}

func (o *out) comment(s string) {
	for _, l := range strings.Split(s, "\n") {
		o.line("# %s", l)
	}
}

func (o *out) open(format string, args ...any) {
	o.line(format+" {", args...)
	o.ind++
}

func (o *out) close() {
	o.ind--
	o.line("}")
}

func (o *out) blank() { o.b.WriteByte('\n') }

// ---- top level ----

var ignoredTop = map[string]string{
	"worker_processes": "LOTUS uses every CPU by itself (see max_procs)", "worker_connections": "connection limits are limits { max_connections }",
	"worker_rlimit_nofile": "", "user": "LOTUS drops privileges by its own settings", "pid": "", "daemon": "", "events": "", "pcre_jit": "",
	"error_log": "LOTUS writes its server log to standard error; a site's error log is logs { error }", "env": "", "thread_pool": "", "timer_resolution": "",
	"worker_priority": "", "worker_cpu_affinity": "", "lock_file": "", "working_directory": "", "ssl_engine": "", "include": "",
}

func (c *conv) top(nodes []*Node) {
	var httpNodes []*Node
	for _, n := range nodes {
		if n.Name == "http" && n.IsCtx {
			httpNodes = append(httpNodes, n)
		}
	}
	if len(httpNodes) == 0 {
		// A fragment (conf.d/site.conf) with server blocks and no http around them.
		c.httpBody = ""
		c.http = &scope{nodes: nodes}
		c.httpLevel(nodes)
		return
	}
	for _, n := range nodes {
		switch n.Name {
		case "http":
			c.http = &scope{nodes: n.Block}
			c.httpLevel(n.Block)
		case "stream", "mail":
			c.flag(nil, n, Unsupported, n.Name+" proxying (TCP, UDP, mail) is not something LOTUS does")
		case "load_module":
			c.flag(nil, n, Unsupported, "Nginx modules can't be loaded; check below for what the module did")
		case "include":
			c.includeNote(n)
		default:
			if why, ok := ignoredTop[n.Name]; ok {
				c.note(n, Ignored, orDefault(why, "no equivalent needed in LOTUS"))
			} else {
				c.flag(nil, n, Unsupported, "not translated")
			}
		}
	}
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func (c *conv) includeNote(n *Node) {
	if len(n.Block) == 1 && n.Block[0].Name == "#error" {
		c.flag(nil, n, Unsupported, "could not read the included file: "+n.Block[0].Args[0])
		return
	}
	if len(n.Args) == 1 && builtinInclude(n.Args[0]) {
		c.note(n, Ignored, n.Args[0]+" is built in: LOTUS knows media types and sets the FastCGI parameters PHP needs")
		return
	}
	c.note(n, Ignored, "included file already read")
}

func (c *conv) httpLevel(nodes []*Node) {
	sc := c.http
	// First the definitions servers refer to, in any order.
	for _, n := range nodes {
		switch n.Name {
		case "upstream":
			c.upstream(n)
		case "proxy_cache_path", "fastcgi_cache_path":
			c.cachePath(n)
		case "limit_req_zone":
			c.limitZone(n)
		}
	}
	c.httpComp = c.compression(sc, nil)
	for _, n := range nodes {
		switch n.Name {
		case "server":
			c.server(n, sc)
		case "upstream", "proxy_cache_path", "fastcgi_cache_path", "limit_req_zone":
		case "include":
			c.includeNote(n)
		default:
			c.contextDirective(nil, sc, n, "http")
		}
	}
	c.buildSites()
	c.finishQuic()
}

// ---- directives that can sit in http or server and mostly set defaults ----

func (c *conv) contextDirective(o *out, sc *scope, n *Node, where string) {
	switch n.Name {
	case "client_max_body_size":
		if where == "http" {
			if v, ok := c.bodyLimit(n); ok {
				c.setLimit("max_request_body", v)
			}
		}
	case "keepalive_timeout":
		if where == "http" && len(n.Args) > 0 {
			if d, ok := duration(n.Args[0]); ok && d != "0s" {
				c.setLimit("idle_timeout", d)
				c.ok(n)
			} else {
				c.note(n, Ignored, "LOTUS keeps connections alive with idle_timeout")
			}
		}
	case "client_header_timeout":
		if where == "http" && len(n.Args) == 1 {
			if d, ok := duration(n.Args[0]); ok {
				c.setLimit("header_timeout", d)
				c.ok(n)
			}
		}
	case "set_real_ip_from":
		for _, a := range n.Args {
			c.addTrusted(n, a)
		}
	case "real_ip_header":
		c.realIPHeader(n)
	case "ssl_protocols":
		c.sslProtocols(n)
	case "gzip", "gzip_types", "gzip_min_length", "gzip_comp_level", "gzip_static":
		// handled together by compression()
	case "error_page", "index", "root", "access_log", "error_log", "add_header", "limit_req", "limit_req_status", "limit_req_zone",
		"proxy_cache", "fastcgi_cache", "proxy_set_header", "proxy_connect_timeout", "proxy_read_timeout", "proxy_cache_valid",
		"proxy_cache_use_stale", "proxy_cache_lock", "proxy_cache_lock_timeout", "proxy_cache_key", "proxy_cache_bypass",
		"proxy_no_cache", "fastcgi_cache_valid", "fastcgi_cache_use_stale", "fastcgi_cache_lock", "fastcgi_cache_lock_timeout",
		"fastcgi_cache_key", "fastcgi_cache_bypass", "fastcgi_no_cache", "fastcgi_read_timeout", "fastcgi_connect_timeout",
		"proxy_pass", "fastcgi_pass", "ssl_certificate", "ssl_certificate_key", "server_name", "listen", "return", "rewrite":
		// inherited: reported where it takes effect
	default:
		c.generic(o, n)
	}
}

// generic classifies a directive by name: harmless, or not translated.
func (c *conv) generic(o *out, n *Node) {
	if why, ok := ignoredDirective(n); ok {
		c.note(n, Ignored, why)
		return
	}
	if why, ok := knownLost[n.Name]; ok {
		c.flag(o, n, Unsupported, why)
		return
	}
	c.flag(o, n, Unsupported, "not translated: LOTUS has no equivalent")
}

var ignoredNames = map[string]string{
	"sendfile": "LOTUS uses the fastest way the system offers", "sendfile_max_chunk": "", "tcp_nopush": "", "tcp_nodelay": "", "aio": "", "directio": "",
	"server_tokens": "LOTUS does not announce its version", "types_hash_max_size": "", "types_hash_bucket_size": "",
	"server_names_hash_bucket_size": "", "server_names_hash_max_size": "", "variables_hash_max_size": "", "variables_hash_bucket_size": "",
	"keepalive_requests": "", "lingering_close": "", "lingering_time": "", "lingering_timeout": "", "reset_timedout_connection": "",
	"log_not_found": "", "log_subrequest": "", "output_buffers": "", "postpone_output": "", "read_ahead": "", "send_lowat": "",
	"ssl_session_cache": "LOTUS chooses TLS session settings itself", "ssl_session_timeout": "", "ssl_session_tickets": "", "ssl_ciphers": "LOTUS uses Go's secure cipher suites",
	"ssl_prefer_server_ciphers": "", "ssl_ecdh_curve": "", "ssl_dhparam": "", "ssl_stapling": "", "ssl_stapling_verify": "", "ssl_trusted_certificate": "",
	"ssl_buffer_size": "", "ssl_early_data": "", "ssl_conf_command": "", "ssl_reject_handshake": "", "http2": "HTTP/2 is on for every https listener",
	"http3": "", "http3_hq": "", "quic_retry": "", "quic_gso": "", "quic_bpf": "", "http2_max_concurrent_streams": "", "http2_idle_timeout": "",
	"proxy_http_version": "LOTUS speaks HTTP/1.1 to backends and upgrades connections itself", "proxy_buffering": "LOTUS streams responses",
	"proxy_buffers": "", "proxy_buffer_size": "", "proxy_busy_buffers_size": "", "proxy_request_buffering": "", "proxy_send_timeout": "",
	"proxy_max_temp_file_size": "", "proxy_temp_path": "", "proxy_ssl_server_name": "", "proxy_ssl_protocols": "", "proxy_ssl_session_reuse": "",
	"fastcgi_param": "LOTUS sets the FastCGI parameters PHP needs", "fastcgi_index": "see index", "fastcgi_split_path_info": "LOTUS splits the script from PATH_INFO itself",
	"fastcgi_buffers": "", "fastcgi_buffer_size": "", "fastcgi_busy_buffers_size": "", "fastcgi_send_timeout": "", "fastcgi_keep_conn": "", "fastcgi_temp_path": "",
	"gzip_vary": "LOTUS always sends Vary: Accept-Encoding", "gzip_proxied": "", "gzip_disable": "", "gzip_buffers": "", "gzip_http_version": "",
	"default_type": "", "log_format": "LOTUS writes its own access-log format", "client_body_buffer_size": "", "client_body_temp_path": "",
	"client_body_timeout": "", "client_header_buffer_size": "", "large_client_header_buffers": "see limits { max_header_bytes }", "send_timeout": "",
	"resolver": "", "resolver_timeout": "", "underscores_in_headers": "", "ignore_invalid_headers": "", "absolute_redirect": "", "port_in_redirect": "",
	"server_name_in_redirect": "", "msie_padding": "", "chunked_transfer_encoding": "", "real_ip_recursive": "", "max_ranges": "", "if_modified_since": "",
	"keepalive_disable": "", "keepalive_time": "", "connection_pool_size": "", "request_pool_size": "", "client_body_in_file_only": "", "limit_rate": "",
	"limit_rate_after": "", "proxy_ignore_client_abort": "", "proxy_next_upstream": "LOTUS retries per upstream: see retries", "proxy_next_upstream_tries": "",
	"proxy_next_upstream_timeout": "", "fastcgi_ignore_client_abort": "", "open_file_cache": "", "open_file_cache_valid": "",
	"open_file_cache_min_uses": "", "open_file_cache_errors": "", "limit_conn_status": "", "limit_conn_log_level": "", "limit_req_log_level": "",
}

func ignoredDirective(n *Node) (string, bool) {
	if why, ok := ignoredNames[n.Name]; ok {
		return orDefault(why, "no equivalent needed in LOTUS"), true
	}
	switch {
	case strings.HasPrefix(n.Name, "ssl_session"), strings.HasPrefix(n.Name, "open_file_cache"), strings.HasPrefix(n.Name, "http2_"),
		strings.HasPrefix(n.Name, "quic_"), strings.HasPrefix(n.Name, "proxy_ssl_"):
		return "no equivalent needed in LOTUS", true
	}
	if n.Name == "proxy_redirect" && len(n.Args) == 1 && (n.Args[0] == "off" || n.Args[0] == "default") {
		return "LOTUS does not rewrite Location headers", true
	}
	return "", false
}

var knownLost = map[string]string{
	"alias":   "alias maps a location to another directory; LOTUS serves one root per site",
	"expires": "LOTUS has no expires directive outside .htaccess (Expires* there)", "auth_basic": "Basic authentication is available through .htaccess (AuthType Basic) only",
	"auth_basic_user_file": "see auth_basic", "autoindex": "LOTUS does not list directories", "set": "variables can't be set; rewrite the logic with route and match",
	"map": "map blocks have no equivalent", "geo": "geo blocks have no equivalent", "split_clients": "no equivalent", "charset": "LOTUS has no charset directive outside .htaccess",
	"auth_request": "subrequest authentication has no equivalent", "limit_conn": "per-location connection limits: only limits { max_connections_per_ip } exists, for the whole server",
	"limit_conn_zone": "see limit_conn", "internal": "a prefix, exact or regex route can't be limited to internal redirects (only @name routes are)",
	"ssl_verify_client": "client certificates are not supported", "ssl_client_certificate": "client certificates are not supported",
	"ssl_crl": "client certificates are not supported", "ssl_verify_depth": "client certificates are not supported",
	"access_by_lua_block": "Lua is not supported", "content_by_lua_block": "Lua is not supported", "sub_filter": "response rewriting has no equivalent",
	"sub_filter_once": "", "sub_filter_types": "", "add_trailer": "response trailers can't be added", "more_set_headers": "third-party header module: use security_headers",
	"mirror": "request mirroring has no equivalent", "image_filter": "no equivalent", "secure_link": "no equivalent", "stub_status": "see the metrics block (Prometheus)",
	"gunzip": "no equivalent", "brotli": "third-party Brotli module: use compression { algorithms [ \"br\" ] }", "brotli_types": "see brotli",
	"brotli_comp_level": "see brotli", "brotli_static": "see compression { precompressed true }", "etag": "ETag is always sent for files", "satisfy": "no equivalent",
	"modsecurity": "WAF is not part of LOTUS 1.0", "modsecurity_rules_file": "WAF is not part of LOTUS 1.0", "proxy_intercept_errors": "error_page applies to errors LOTUS produces, not to backend responses",
	"fastcgi_intercept_errors": "error_page applies to errors LOTUS produces, not to PHP responses", "proxy_set_body": "no equivalent", "proxy_hide_header": "no equivalent",
	"proxy_pass_header": "no equivalent", "proxy_ignore_headers": "no equivalent", "fastcgi_hide_header": "no equivalent", "fastcgi_ignore_headers": "no equivalent",
	"merge_slashes": "slash merging is part of path normalization", "disable_symlinks": "see the symlinks setting of a site", "types": "media types are built in",
	"xslt_stylesheet": "no equivalent", "ssi": "server side includes are not supported", "uwsgi_pass": "uwsgi is not supported", "scgi_pass": "SCGI is not supported",
	"grpc_pass": "gRPC proxying is not supported", "memcached_pass": "no equivalent", "perl": "no equivalent", "perl_set": "no equivalent",
	"valid_referers": "no equivalent", "referer_hash_bucket_size": "", "zone_sync": "no equivalent",
	"mp4": "streaming modules are not supported", "flv": "streaming modules are not supported", "slice": "slice requests are not supported",
	"proxy_cache_methods": "only GET and HEAD are cached", "proxy_cache_min_uses": "no equivalent", "proxy_cache_revalidate": "LOTUS does not revalidate cached responses yet",
	"proxy_cache_background_update": "stale-while-revalidate is not implemented", "proxy_cache_convert_head": "", "proxy_store": "no equivalent",
	"proxy_bind": "no equivalent", "proxy_cookie_domain": "no equivalent", "proxy_cookie_path": "no equivalent", "proxy_method": "no equivalent",
	"fastcgi_store": "no equivalent", "fastcgi_cache_revalidate": "LOTUS does not revalidate cached responses yet", "fastcgi_cache_methods": "only GET and HEAD are cached",
	"fastcgi_cache_min_uses": "no equivalent", "fastcgi_cache_background_update": "stale-while-revalidate is not implemented",
}

// setLimit records a server-wide limit; a later value replaces an earlier one.
func (c *conv) setLimit(name, value string) {
	for i := range c.httpLimit {
		if c.httpLimit[i][0] == name {
			c.httpLimit[i][1] = value
			return
		}
	}
	c.httpLimit = append(c.httpLimit, [2]string{name, value})
}

// ---- real client address, TLS floor ----

func (c *conv) addTrusted(n *Node, v string) {
	if v == "unix:" {
		c.note(n, Ignored, "unix sockets are not used for the client address")
		return
	}
	cidr := v
	if !strings.Contains(v, "/") {
		ip := net.ParseIP(v)
		if ip == nil {
			c.flag(nil, n, Unsupported, fmt.Sprintf("%q is not an address or network", v))
			return
		}
		bits := 128
		if ip.To4() != nil {
			bits = 32
		}
		cidr = fmt.Sprintf("%s/%d", v, bits)
	}
	if _, _, err := net.ParseCIDR(cidr); err != nil {
		c.flag(nil, n, Unsupported, fmt.Sprintf("%q is not a network", v))
		return
	}
	if !c.tsSeen[cidr] {
		c.tsSeen[cidr] = true
		c.trusted = append(c.trusted, cidr)
	}
	c.ok(n)
}

func (c *conv) realIPHeader(n *Node) {
	if len(n.Args) == 1 && strings.EqualFold(n.Args[0], "X-Forwarded-For") {
		c.ok(n)
		return
	}
	c.flag(nil, n, Approximated, "LOTUS reads the client address from X-Forwarded-For and Forwarded (and PROXY protocol) of trusted_proxies only")
}

func (c *conv) sslProtocols(n *Node) {
	min := "1.3"
	for _, a := range n.Args {
		switch strings.ToLower(a) {
		case "tlsv1.2":
			if min == "1.3" {
				min = "1.2"
			}
		case "tlsv1", "tlsv1.1", "sslv3":
			min = "1.2"
			c.flag(nil, n, Approximated, a+" is not supported: LOTUS serves TLS 1.2 and 1.3 only")
		}
	}
	if c.tlsLine == nil || min == "1.2" {
		c.tlsMin, c.tlsLine = min, n
	}
	c.ok(n)
}

func (c *conv) bodyLimit(n *Node) (string, bool) {
	if len(n.Args) != 1 {
		return "", false
	}
	s, v, ok := size(n.Args[0])
	if !ok {
		c.flag(nil, n, Unsupported, fmt.Sprintf("%q is not a size", n.Args[0]))
		return "", false
	}
	if v == 0 {
		c.flag(nil, n, Approximated, "Nginx's 0 means no limit; LOTUS always has one (10mib by default), so the default stays")
		return "", false
	}
	c.ok(n)
	return s, true
}

// ---- upstream ----

func (c *conv) upstream(n *Node) {
	if len(n.Args) != 1 {
		c.flag(nil, n, Unsupported, "an upstream block needs a name")
		return
	}
	u := &upstream{name: n.Args[0], explicit: true, line: n.Line, file: n.File}
	if !validName(u.name) {
		c.flag(nil, n, Unsupported, fmt.Sprintf("the upstream name %q can't be used as it is", u.name))
		return
	}
	for _, d := range n.Block {
		switch d.Name {
		case "server":
			c.upstreamServer(u, d)
		case "least_conn":
			u.balance = "least_conn"
			c.ok(d)
		case "ip_hash":
			u.balance = "ip_hash"
			c.ok(d)
		case "random":
			u.balance = "random"
			c.ok(d)
		case "hash":
			if len(d.Args) >= 1 && (d.Args[0] == "$request_uri" || d.Args[0] == "$uri") {
				u.balance = "uri_hash"
				c.ok(d)
			} else {
				c.flag(nil, d, Approximated, "only hashing by the request URI is supported (uri_hash); round_robin is used")
			}
		case "keepalive", "keepalive_requests", "zone", "queue", "sticky", "ntlm", "state":
			c.note(d, Ignored, "no equivalent needed in LOTUS")
		case "keepalive_timeout":
			if len(d.Args) == 1 {
				if v, ok := duration(d.Args[0]); ok {
					u.idle = v
					c.ok(d)
				}
			}
		default:
			c.generic(nil, d)
		}
	}
	if len(u.backends) == 0 {
		c.flag(nil, n, Unsupported, "the upstream has no usable server")
		return
	}
	if _, dup := c.umap[u.name]; dup {
		c.flag(nil, n, Unsupported, "an upstream with this name was already defined")
		return
	}
	c.umap[u.name] = u
	c.ups = append(c.ups, u)
	c.ok(n)
}

var validNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func validName(s string) bool { return validNameRe.MatchString(s) }

func (c *conv) upstreamServer(u *upstream, d *Node) {
	if len(d.Args) == 0 {
		return
	}
	addr := d.Args[0]
	b := backend{weight: 1}
	down := false
	for _, a := range d.Args[1:] {
		k, v, _ := strings.Cut(a, "=")
		switch k {
		case "weight":
			if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 1000 {
				b.weight = n
			}
		case "max_conns":
			if n, err := strconv.Atoi(v); err == nil && n >= 1 {
				b.maxConns = n
			}
		case "max_fails":
			if u.maxFails == "" {
				u.maxFails = v
			} else if u.maxFails != v {
				c.flag(nil, d, Approximated, "max_fails is per upstream in LOTUS; the first server's value is used")
			}
		case "fail_timeout":
			if t, ok := duration(v); ok {
				if u.failTimeout == "" {
					u.failTimeout = t
				} else if u.failTimeout != t {
					c.flag(nil, d, Approximated, "fail_timeout is per upstream in LOTUS; the first server's value is used")
				}
			}
		case "backup":
			c.flag(nil, d, Approximated, "backup servers don't exist; this one takes part like the others")
		case "down":
			down = true
		case "resolve", "route", "service", "slow_start":
			c.flag(nil, d, Approximated, k+" is not supported and was dropped")
		}
	}
	if down {
		c.note(d, Ignored, "the server is marked down, so it is left out")
		return
	}
	if strings.HasPrefix(addr, "unix:") {
		b.addr = addr
	} else {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			host, port = strings.Trim(addr, "[]"), "80"
		}
		if host == "" {
			c.flag(nil, d, Unsupported, "no host in the server address")
			return
		}
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		b.addr = host + ":" + port
	}
	u.backends = append(u.backends, b)
	c.ok(d)
}

func (c *conv) backendURL(u *upstream, b backend) string {
	if strings.HasPrefix(b.addr, "unix:") {
		return b.addr
	}
	scheme := "http"
	if u.https {
		scheme = "https"
	}
	return scheme + "://" + b.addr
}

// ---- cache zones, rate limit zones ----

var keysZoneRe = regexp.MustCompile(`^([A-Za-z0-9_.-]+):(\d+[kKmMgG]?)$`)

func (c *conv) cachePath(n *Node) {
	var name, max string
	for _, a := range n.Args {
		k, v, ok := strings.Cut(a, "=")
		if !ok {
			continue
		}
		switch k {
		case "keys_zone":
			if m := keysZoneRe.FindStringSubmatch(v); m != nil {
				name = m[1]
			}
		case "max_size":
			if s, _, ok := size(v); ok {
				max = s
			}
		}
	}
	if name == "" {
		c.flag(nil, n, Unsupported, "no usable keys_zone")
		return
	}
	if _, dup := c.cmap[name]; dup {
		return
	}
	z := &cacheZone{name: name, maxSize: max}
	c.cmap[name] = z
	c.caches = append(c.caches, z)
	c.flag(nil, n, Approximated, "LOTUS keeps cached responses in memory (there is no disk cache yet), so max_size is a memory budget")
}

var rateRe = regexp.MustCompile(`^(\d+)r/([sm])$`)

func (c *conv) limitZone(n *Node) {
	var key, zone, rate string
	if len(n.Args) >= 1 {
		key = n.Args[0]
	}
	for _, a := range n.Args[1:] {
		if v, ok := strings.CutPrefix(a, "zone="); ok {
			zone, _, _ = strings.Cut(v, ":")
		}
		if v, ok := strings.CutPrefix(a, "rate="); ok {
			rate = v
		}
	}
	m := rateRe.FindStringSubmatch(rate)
	if zone == "" || m == nil {
		c.flag(nil, n, Unsupported, "no usable zone and rate")
		return
	}
	if key != "$binary_remote_addr" && key != "$remote_addr" {
		c.flag(nil, n, Unsupported, "LOTUS limits by client address only; the key "+key+" has no equivalent")
		return
	}
	req, _ := strconv.Atoi(m[1])
	per := "1s"
	if m[2] == "m" {
		per = "1m"
	}
	c.zones[zone] = &limitZone{requests: req, per: per, ok: true}
	c.ok(n)
}

// ---- rendering ----

func (c *conv) render() string {
	var o out
	o.line("# Translated from an Nginx configuration by `lotus compat nginx convert`.")
	o.line("# Check it with `lotus config validate`, and read the comments marked")
	o.line("# \"approximated\" or \"unsupported\": they are the places where LOTUS does")
	o.line("# not behave exactly as Nginx did.")
	o.blank()
	o.line("lotus_config_version 1")
	o.blank()
	o.open("server")

	if len(c.listeners) == 0 {
		// LOTUS needs a listener; say plainly that this one is not from Nginx.
		o.comment("No server of the Nginx configuration could be translated. This listener is a placeholder.")
		o.open("listen %s", quote("http"))
		o.line("address %s", quote("127.0.0.1:8080"))
		o.close()
		o.blank()
		c.issues = append(c.issues, Issue{Directive: "(configuration)", Status: Unsupported, Message: "no server block could be translated; a placeholder listener was written so the file stays valid"})
	}
	for _, l := range c.listeners {
		o.open("listen %s", quote(l.name))
		o.line("address %s", quote(l.addr))
		if l.https {
			o.line("protocol https")
		}
		if l.http3 {
			o.line("http3 true")
		}
		o.close()
		o.blank()
	}
	if len(c.trusted) > 0 {
		o.line("trusted_proxies [ %s ]", joinQuoted(c.trusted))
		o.blank()
	}
	if c.tlsMin != "" {
		o.open("tls")
		o.line("min_version %s", quote(c.tlsMin))
		o.close()
		o.blank()
	}
	if len(c.httpLimit) > 0 {
		o.open("limits")
		for _, l := range c.httpLimit {
			o.line("%s %s", l[0], l[1])
		}
		o.close()
		o.blank()
	}
	if c.httpComp != nil {
		c.httpComp.write(&o)
		o.blank()
	}
	for _, u := range c.ups {
		c.writeUpstream(&o, u)
		o.blank()
	}
	for _, p := range c.php {
		o.open("php_backend %s", quote(p.name))
		o.line("fastcgi_pass %s", quote(p.pass))
		if p.timeout != "" {
			o.line("timeout %s", p.timeout)
		}
		o.close()
		o.blank()
	}
	for _, z := range c.caches {
		if z.maxSize == "" {
			o.line("cache %s", quote(z.name))
			o.blank()
			continue
		}
		o.open("cache %s", quote(z.name))
		o.line("max_size %s", z.maxSize)
		o.close()
		o.blank()
	}
	sort.SliceStable(c.sites, func(i, j int) bool { return c.sites[i].def && !c.sites[j].def })
	for _, s := range c.sites {
		o.b.WriteString(s.body)
		o.blank()
	}
	o.close()
	return strings.TrimRight(o.b.String(), "\n") + "\n"
}

func joinQuoted(l []string) string {
	q := make([]string, len(l))
	for i, s := range l {
		q[i] = quote(s)
	}
	return strings.Join(q, ", ")
}

func (c *conv) writeUpstream(o *out, u *upstream) {
	o.open("upstream %s", quote(u.name))
	for _, b := range u.backends {
		if b.weight == 1 && b.maxConns == 0 {
			o.line("backend %s", quote(c.backendURL(u, b)))
			continue
		}
		o.open("backend %s", quote(c.backendURL(u, b)))
		if b.weight != 1 {
			o.line("weight %d", b.weight)
		}
		if b.maxConns > 0 {
			o.line("max_conns %d", b.maxConns)
		}
		o.close()
	}
	if u.balance != "" {
		o.line("balance %s", u.balance)
	}
	if u.connect != "" {
		o.line("connect_timeout %s", u.connect)
	}
	if u.response != "" {
		o.line("response_timeout %s", u.response)
	}
	if u.idle != "" {
		o.line("idle_timeout %s", u.idle)
	}
	if u.maxFails != "" {
		o.line("max_fails %s", u.maxFails)
	}
	if u.failTimeout != "" {
		o.line("fail_timeout %s", u.failTimeout)
	}
	o.close()
}
