package schema

import (
	"fmt"
	"regexp"
	"time"

	"github.com/laikait/lotus/internal/config/ast"
	"github.com/laikait/lotus/internal/config/diag"
)

const since = "0.1"

// scalar builds a Single directive with one argument.
func scalar(name, argName string, t Type, def, desc, security string) *Directive {
	return &Directive{
		Name: name, Card: Single, Args: []Arg{{Name: argName, Type: t}},
		Since: since, Default: def, Description: desc, Security: security,
	}
}

// container builds a Single directive that takes a required block.
func container(name string, opens Context, desc, security string) *Directive {
	return &Directive{
		Name: name, Card: Single, Block: BlockRequired, Opens: opens,
		Since: since, Default: "none", Description: desc, Security: security,
	}
}

func child(d *ast.Directive, name string) *ast.Directive {
	if d.Block == nil {
		return nil
	}
	for _, s := range d.Block.Statements {
		if c, ok := s.(*ast.Directive); ok && c.Name == name {
			return c
		}
	}
	return nil
}

var headerNameRe = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

func validHeaderValue(s string) bool {
	for _, r := range s {
		if r < 0x20 && r != '\t' || r == 0x7f {
			return false
		}
	}
	return true
}

var defaultRegistry = buildDefault()

// Default returns the registry of all directives LOTUS currently supports.
// It is built once; callers must not modify it.
func Default() *Registry { return defaultRegistry }

func buildDefault() *Registry {
	r := NewRegistry()

	// ---- shared directives ----

	limits := &Directive{
		Name: "limits", Card: Single, Block: BlockRequired, Opens: CtxLimits, TenantForbidden: true,
		Since: since, Default: "see the directives inside",
		Description: "Connection, size and timeout limits. In a site, they override the server-wide values one by one.",
		Security:    "These limits protect against resource exhaustion (slowloris, huge uploads). Raising them weakens that protection, so only administrators may change them.",
	}
	index := &Directive{
		Name: "index", Card: Single, Args: []Arg{{Name: "files", Type: List(String(), 1)}},
		Since: since, Default: `[ "index.html" ]`,
		Description: "Files to try, in order, when a request names a directory.",
		Security:    "None.",
	}
	root := scalar("root", "path", Path(), "none",
		"Document root: the directory that URLs are mapped into.",
		"Everything below this directory can be served. Pointing it at a directory with secrets exposes them; LOTUS still refuses to follow paths outside it according to the symlink policy.")
	root.TenantForbidden = true
	phpDeny := scalar("php_deny", "enabled", Bool(), "false",
		"When true, PHP files under this route are never executed, even if a PHP backend is configured.",
		"Use it on upload directories so a file uploaded by a visitor can't be run as code.")
	ret := &Directive{
		Name: "return", Card: Single, Placeholders: true,
		Args:  []Arg{{Name: "status", Type: Int(100, 599)}, {Name: "target", Type: String(), Optional: true}},
		Since: since, Default: "none",
		Description: "Answer immediately with a status code. For redirect statuses (301, 302, 303, 307, 308) the second argument is the target URL; otherwise it is the response body.",
		Security:    "A redirect target built from {placeholders} can become an open redirect if it uses a client-controlled value such as {query.next}. CR/LF characters in placeholder values are rejected.",
	}
	tryFiles := &Directive{
		Name: "try_files", Card: Single, Placeholders: true,
		Args:  []Arg{{Name: "candidates", Type: List(String(), 1)}},
		Since: since, Default: "none",
		Description: "Tries each candidate as a file or directory in the site root. If none exists, the last entry is handled as an internal redirect (or \"=404\" returns that status).",
		Security:    "Candidates are resolved inside the document root only. The last entry re-enters routing, so it can reach any route of the site.",
	}
	rewrite := &Directive{
		Name: "rewrite", Card: Repeated, Placeholders: true,
		Args: []Arg{
			{Name: "pattern", Type: Regex(), Raw: true},
			{Name: "target", Type: String()},
			{Name: "flag", Type: Enum("last", "redirect", "permanent"), Optional: true},
		},
		Since: since, Default: "none",
		Description: "If the request path matches the pattern, replaces the request: with last (the default) routing starts again with the new URI, redirect answers 302 and permanent answers 301. Rewrites are tried in order and the first match wins; they run before return and try_files. In the target, {re.1}, {re.2}... are the pattern's capture groups.",
		Security:    "A redirect target built from {re.N} can become an open redirect if the pattern lets a visitor choose the host, as in '^/go/(.*)$' to \"{re.1}\". Start the target with \"/\" or a fixed host. CR/LF characters in captured values are rejected.",
	}
	errorPage := &Directive{
		Name: "error_page", Card: Repeated,
		Args: []Arg{
			{Name: "status", Type: Int(400, 599)},
			{Name: "target", Type: String()},
		},
		Since: since, Default: "none",
		Description: "Shows another page for responses with this status that LOTUS produces itself (a missing file, return or try_files =404). The target is a path such as \"/404.html\" or a named route such as \"@error\". The visitor still gets the original status code. Routes inherit the site's error pages and can replace them one by one.",
		Security:    "The error page is read from the same document root, under the same rules as any file. An error page that itself fails is not replaced again, so there are no loops.",
		Validate:    validateErrorPage,
	}
	php := scalar("php", "backend", String(), "none",
		"Runs matching requests through the named php_backend over FastCGI.",
		"Only existing files below the document root are ever passed to PHP. Which backend (and so which pool and OS user) runs a site is an administrator decision.")
	php.Args[0].Ref = "php_backend"
	php.TenantForbidden = true
	maxBody := scalar("max_request_body", "size", Size(1), "10mib",
		"Largest request body accepted. Bigger bodies get 413 before they are read.",
		"Limits memory and disk use from uploads. Raise it only for the routes that need it.")
	secHeaders := &Directive{
		Name: "security_headers", Card: Single, Block: BlockRequired, Opens: CtxSecurityHeaders,
		Since: since, Default: "none",
		Description: "Response headers added to every response, written as \"Name\" = \"value\" entries.",
		Security:    "Headers such as Content-Security-Policy and Strict-Transport-Security harden browsers against attacks. Values can't contain control characters, so they can't inject extra headers.",
	}

	// ---- root ----

	r.Define(CtxRoot, &Directive{
		Name: "server", Card: Single, Block: BlockRequired, Opens: CtxServer, TenantForbidden: true,
		Since: since, Default: "none",
		Description: "Top-level container for listeners, TLS settings, PHP backends and sites.",
		Security:    "Defines everything LOTUS serves. Only administrators should be able to edit the file that contains it.",
	}).Required = []string{"server"}

	// ---- server ----

	r.Define(CtxServer,
		container("runtime", CtxRuntime, "Go runtime settings for the LOTUS process.", "None.").withForbidden(),
		&Directive{
			Name: "listen", Card: Keyed, Args: []Arg{{Name: "name", Type: String()}},
			Block: BlockRequired, Opens: CtxListen, Defines: "listener", TenantForbidden: true,
			Since: since, Default: "none",
			Description: "A network listener. Sites attach to listeners by name.",
			Security:    "Binding to 0.0.0.0 or [::] exposes the listener on every network interface. Ports below 1024 need special privileges (see the privilege model).",
			Validate:    validateListen,
		},
		&Directive{
			Name: "trusted_proxies", Card: Single, Args: []Arg{{Name: "networks", Type: List(CIDR(), 1)}},
			TenantForbidden: true,
			Since:           since, Default: "none",
			Description: "Networks whose X-Forwarded-For / Forwarded headers and PROXY protocol headers are believed when working out the client IP.",
			Security:    "Listing a network that clients can reach lets them fake their IP address and bypass IP allow-lists and rate limits. List only your own load balancers.",
		},
		limits,
		&Directive{
			Name: "tls", Card: Single, Block: BlockRequired, Opens: CtxTLS, TenantForbidden: true,
			Since: since, Default: "see the directives inside",
			Description: "Server-wide TLS settings.",
			Security:    "Weak TLS settings expose traffic to downgrade and decryption attacks.",
		},
		&Directive{
			Name: "php_backend", Card: Keyed, Args: []Arg{{Name: "name", Type: String()}},
			Block: BlockRequired, Opens: CtxPHPBackend, Defines: "php_backend", TenantForbidden: true,
			Since: since, Default: "none",
			Description: "A FastCGI backend (for example a PHP-FPM pool). Routes use it with the php directive.",
			Security:    "Use one pool, with its own OS user, per site. Sites that share a pool can read each other's files and sessions.",
		},
		&Directive{
			Name: "site", Card: Keyed, Args: []Arg{{Name: "name", Type: Hostname()}},
			Block: BlockRequired, Opens: CtxSite, TenantForbidden: true,
			Since: since, Default: "none",
			Description: "A virtual host. The name is its primary hostname; aliases add more.",
			Security:    "Sites are separated by hostname only. LOTUS reads every site's static files as its own user, so isolate tenants with file permissions and the symlink policy.",
			Validate:    validateSite,
		},
		container("control", CtxControl, "Settings for the local control channel used by the lotus command.", "Anyone who can connect to the control channel can reload or stop the server.").withForbidden(),
	).Required = []string{"listen"}

	// ---- runtime ----

	r.Define(CtxRuntime,
		scalar("max_procs", "count", OneOf(IntAtLeast(1), Enum("auto")), "auto",
			"Maximum number of CPUs Go uses (GOMAXPROCS). auto follows the CPU count and container limits.",
			"None."),
		scalar("memory_limit", "size", Size(16<<20), "none",
			"Soft memory limit for the Go runtime (GOMEMLIMIT). The garbage collector works harder as usage approaches it.",
			"Set it below the container or machine limit so the process is not killed by the OS."),
	)

	// ---- listen ----

	r.Define(CtxListen,
		scalar("address", "address", Address(HostPort), "none",
			"Address to listen on, as host:port, [ipv6]:port or :port.",
			"0.0.0.0 and [::] accept connections from every interface.",
		),
		scalar("protocol", "protocol", Enum("http", "https"), "http",
			"http serves plain HTTP; https serves TLS (HTTP/1.1 and HTTP/2).",
			"Plain HTTP is not encrypted. Redirect it to HTTPS or use it only on trusted networks."),
		scalar("http3", "enabled", Bool(), "false",
			"Also serve HTTP/3 over UDP on the same port number and advertise it with Alt-Svc. Requires protocol https.",
			"Needs the UDP port open in the firewall. QUIC has its own limits and tuning needs (see operations docs)."),
	).Required = []string{"address"}

	// ---- limits ----

	r.Define(CtxLimits,
		scalar("max_connections", "count", IntAtLeast(1), "10000",
			"Maximum number of simultaneous connections. New connections beyond it are closed.",
			"Too high a value lets clients exhaust file descriptors and memory."),
		maxBody,
		scalar("max_header_bytes", "size", Size(1<<10), "64kib",
			"Maximum total size of the request line and headers. A request over the limit gets 431. Go's HTTP server reads a little more before it stops (about 4 KiB), so the effective limit is slightly above this value.",
			"Large values make header-based memory exhaustion easier."),
		scalar("header_timeout", "duration", Duration(time.Millisecond), "15s",
			"Time allowed to receive the request headers after a connection is established.",
			"Long values make slowloris attacks easier."),
		scalar("read_timeout", "duration", Duration(time.Millisecond), "60s",
			"Time allowed to receive the whole request.", "Long values make slow-upload attacks easier."),
		scalar("write_timeout", "duration", Duration(time.Millisecond), "60s",
			"Time allowed to send a response.", "Long values let slow readers hold connections open."),
		scalar("idle_timeout", "duration", Duration(time.Millisecond), "120s",
			"Time an idle keep-alive connection is kept open.", "Long values let idle clients hold connections."),
	)

	// ---- tls ----

	r.Define(CtxTLS,
		scalar("min_version", "version", Enum("1.2", "1.3"), "1.2",
			"Lowest TLS version accepted. TLS 1.0 and 1.1 are not supported.",
			"Raising it to 1.3 blocks older clients but removes legacy cipher suites."),
		&Directive{
			Name: "acme", Card: Single, Block: BlockRequired, Opens: CtxACME, TenantForbidden: true,
			Since: since, Default: "none",
			Description: "Automatic certificate issuance and renewal through ACME (for example Let's Encrypt).",
			Security:    "The account key controls certificate issuance for your domains. It is stored under the LOTUS data directory and must stay private.",
		},
	)
	r.Define(CtxACME,
		scalar("email", "address", String(), "none",
			"Contact email registered with the certificate authority.",
			"Used by the CA for expiry and problem notices."),
		scalar("ca", "url", String(), "Let's Encrypt production",
			"ACME directory URL of the certificate authority.",
			"Use a staging CA while testing to avoid production rate limits."),
		scalar("eab_key_id", "id", String(), "none",
			"External account binding key ID, for CAs that require it.", "Treat as a credential."),
		func() *Directive {
			d := scalar("eab_hmac_key", "key", String(), "none",
				"External account binding HMAC key, for CAs that require it.",
				"A credential: it is redacted in all output. Prefer env(\"NAME\") over writing it into the file.")
			d.Secret = true
			return d
		}(),
	)

	// ---- site ----

	listeners := &Directive{
		Name: "listeners", Card: Single, TenantForbidden: true,
		Args:  []Arg{{Name: "names", Type: List(String(), 1), Ref: "listener"}},
		Since: since, Default: "all listeners",
		Description: "Names of the listeners this site answers on.",
		Security:    "A site attached to a plain-HTTP listener can be read and altered on the network.",
	}
	aliases := &Directive{
		Name: "aliases", Card: Single, TenantForbidden: true,
		Args:  []Arg{{Name: "hostnames", Type: List(Hostname(), 1)}},
		Since: since, Default: "none",
		Description: "Additional hostnames served by this site. Wildcards like *.example.com are allowed.",
		Security:    "Whoever controls the aliases controls which hostnames reach this site, so tenants must not edit them.",
	}
	siteTLS := scalar("tls", "mode", Enum("automatic", "manual", "off"), "automatic when the listener is https",
		"automatic obtains certificates through ACME; manual uses certificates you provide; off serves no TLS for this site.",
		"With off, this site is served without encryption even on an https listener where possible.")
	siteTLS.TenantForbidden = true
	certificate := scalar("certificate", "path", Path(), "none",
		"PEM certificate chain file for tls manual. The certificate is chosen by SNI when the site name or an alias matches.",
		"The file is read once at start and on reload.")
	certificate.TenantForbidden = true
	certificateKey := scalar("certificate_key", "path", Path(), "none",
		"PEM private key file that belongs to certificate.",
		"The key must be readable only by the user that starts LOTUS (before it drops privileges). Anyone who can read it can impersonate the site.")
	certificateKey.TenantForbidden = true
	symlinks := scalar("symlinks", "policy", Enum("follow", "owner_match", "deny"), "owner_match",
		"How symbolic links below the document root are treated: deny refuses them, owner_match follows a link only if the link and its target have the same owner, follow allows them. A link that leads outside the document root is always refused, and so is every absolute link, even one that points back inside; use relative links.",
		"On shared hosts keep owner_match or deny so one tenant can not link to another tenant's files.")
	symlinks.TenantForbidden = true
	dotfiles := scalar("dotfiles", "policy", Enum("deny", "allow"), "deny",
		"Whether files and directories whose names start with a dot (such as .git or .env) can be served. /.well-known/ is always allowed.",
		"Dotfiles often hold secrets and repository data. Allow them only if you are sure.")
	dotfiles.TenantForbidden = true
	route := &Directive{
		Name: "route", Card: Repeated, Block: BlockRequired, Opens: CtxRoute,
		Args: []Arg{
			{Name: "operator", Type: Operator("=", "^~", "~", "~*"), Optional: true},
			{Name: "path", Type: String(), Raw: true},
		},
		Since: since, Default: "none",
		Description: "Handles requests whose path matches. See the routing precedence document for how routes are chosen.",
		Security:    "A regular-expression route is tried before shorter prefix routes. A broad pattern can capture requests meant for a protected prefix; mark protected prefixes with ^~.",
		Validate:    validateRoute,
	}
	logs := &Directive{
		Name: "logs", Card: Single, Block: BlockRequired, Opens: CtxLogs, TenantForbidden: true,
		Since: since, Default: "none",
		Description: "Per-site log files.",
		Security:    "LOTUS writes these files as its own user. A site's log path must not point at files it should not be able to modify.",
	}
	siteLimits := *limits
	r.Define(CtxSite,
		listeners, aliases, siteTLS, certificate, certificateKey, symlinks, dotfiles, root, index, route, logs, &siteLimits, secHeaders,
		errorPage,
	)

	// ---- route ----

	routeIndex := *index
	routeLimits := *maxBody
	r.Define(CtxRoute,
		route, tryFiles, php, phpDeny, ret, rewrite, errorPage, &routeIndex, &routeLimits,
		&Directive{
			Name: "match", Card: Repeated, Block: BlockRequired, Opens: CtxMatch,
			Since: since, Default: "none",
			Description: "Applies different actions when extra conditions hold. The first match block whose conditions all hold wins; its actions replace the route's own.",
			Security:    "Conditions on client_ip use the resolved client IP, which depends on trusted_proxies being correct.",
			Validate:    validateMatch,
		},
	)
	root2 := *root
	r.Define(CtxRoute, &root2)

	// ---- match ----

	patternCond := func(name, desc string) *Directive {
		return &Directive{
			Name: name, Card: Repeated, Validate: validatePatternCondition,
			Args: []Arg{
				{Name: "name", Type: String()},
				{Name: "operator", Type: Operator("=", "~", "~*", "!~", "!~*"), Optional: true},
				{Name: "value", Type: String(), Raw: true},
			},
			Since: since, Default: "none", Description: desc,
			Security: "Values come from the client and can be forged; don't use them alone for access control.",
		}
	}
	r.Define(CtxMatch,
		scalar("method", "methods", List(Enum("GET", "HEAD", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"), 1), "none",
			"True if the request method is one of these.", "None."),
		&Directive{
			Name: "client_ip", Card: Single, Since: since, Default: "none",
			Args: []Arg{
				{Name: "operator", Type: Operator("=", "!~"), Optional: true},
				{Name: "networks", Type: List(CIDR(), 1)},
			},
			Description: "True if the client IP is in one of the networks (with !~: if it is in none of them).",
			Security:    "Behind a proxy the client IP is only correct when trusted_proxies lists that proxy.",
		},
		patternCond("header", "Tests a request header."),
		patternCond("query", "Tests a query-string parameter."),
		patternCond("cookie", "Tests a cookie."),
		scalar("scheme", "scheme", Enum("http", "https"), "none", "True if the request used this scheme.", "None."),
		scalar("host", "hostnames", List(Hostname(), 1), "none", "True if the Host header is one of these.", "None."),
		// actions
		ret, tryFiles, php, phpDeny,
	)

	// ---- logs ----

	r.Define(CtxLogs,
		scalar("access", "path", Nullable(Path()), "none",
			"File that receives the access log. null turns it off.",
			"The file is opened as the LOTUS user; access logs contain client IPs and URLs (personal data)."),
		scalar("error", "path", Nullable(Path()), "server error log",
			"File that receives this site's error log. null turns it off.",
			"Error logs can contain request details."),
	)

	// ---- php_backend ----

	phpb := r.Define(CtxPHPBackend,
		scalar("fastcgi_pass", "address", Address(HostPort|UnixSock), "none",
			"FastCGI server address: unix:/path/to/socket or host:port. For a server that runs on its own, such as PHP-FPM.",
			"Prefer Unix sockets with restricted permissions. A TCP address that is reachable by others lets them run PHP as this pool."),
		scalar("command", "path", Path(), "none",
			"A php-cgi program that LOTUS starts and supervises instead of connecting to an existing server. Use this where there is no PHP-FPM, such as Windows.",
			"The processes run as the LOTUS user and get a minimal environment (no LOTUS secrets). On Windows they listen on 127.0.0.1, so any account on the machine can reach them."),
		scalar("processes", "count", Int(1, 256), "4",
			"How many php-cgi processes to run with command. Each handles one request at a time.",
			"More processes allow more simultaneous requests and use more memory."),
		scalar("max_requests", "count", IntAtLeast(0), "500",
			"A php-cgi process exits and is replaced after this many requests, which contains memory leaks. 0 means never.",
			"Applies to command."),
		scalar("php_ini", "path", Path(), "none",
			"php.ini file for the processes started with command (php-cgi -c). Without it PHP searches its usual places.",
			"The file decides which PHP settings apply, including open_basedir and disabled functions."),
		scalar("timeout", "duration", Duration(time.Millisecond), "60s",
			"Time to wait for the FastCGI server to respond. With command it is also how long a request waits for a free process.",
			"Long timeouts let slow scripts tie up connections."),
	)
	phpb.Check = func(present map[string]bool) (string, string, string) {
		switch {
		case present["fastcgi_pass"] && present["command"]:
			return diag.CodeInvalidValue, "a php_backend has either fastcgi_pass or command, not both",
				"use fastcgi_pass for a PHP server that runs on its own, command to have LOTUS start php-cgi"
		case !present["fastcgi_pass"] && !present["command"]:
			return diag.CodeMissingRequired, "a php_backend needs fastcgi_pass or command",
				`example: fastcgi_pass "unix:/run/php/php-fpm.sock"   or   command "C:/php/php-cgi.exe"`
		case present["fastcgi_pass"]:
			for _, d := range []string{"processes", "max_requests", "php_ini"} {
				if present[d] {
					return diag.CodeInvalidValue, d + " only applies to command, not to fastcgi_pass", "remove it, or start the processes with command"
				}
			}
		}
		return "", "", ""
	}

	// ---- security_headers ----

	r.Define(CtxSecurityHeaders).Pairs = &PairSpec{
		Key: String(), Value: String(), CaseInsensitive: true,
		Validate: func(key *ast.String, value ast.Value) *Problem {
			name, _ := key.Plain()
			if !headerNameRe.MatchString(name) {
				return &Problem{Msg: fmt.Sprintf("%q is not a valid header name", name), Pos: key.Pos}
			}
			if v, ok := plainString(value); ok && !validHeaderValue(v) {
				return &Problem{Msg: "header values must not contain control characters", Pos: value.Position()}
			}
			return nil
		},
	}

	// ---- control ----

	r.Define(CtxControl,
		scalar("socket", "path", Path(), "/run/lotus/control.sock",
			"Path of the control socket (named pipe name on Windows).",
			"The socket's directory must not be writable by others; access is limited by file permissions."),
		scalar("viewer_group", "group", String(), "none",
			"OS group whose members get read-only access to the control channel.",
			"Members can read status and configuration (secrets are redacted) but can't change anything."),
	)

	return r
}

func (d *Directive) withForbidden() *Directive {
	d.TenantForbidden = true
	return d
}

func validateListen(d *ast.Directive, rep Reporter) {
	h3 := child(d, "http3")
	if h3 == nil || len(h3.Args) != 1 {
		return
	}
	if on, ok := h3.Args[0].(*ast.Bool); !ok || !on.Value {
		return
	}
	proto := "http"
	if p := child(d, "protocol"); p != nil && len(p.Args) == 1 {
		switch v := p.Args[0].(type) {
		case *ast.Ident:
			proto = v.Name
		case *ast.String:
			proto, _ = v.Plain()
		}
	}
	if proto != "https" {
		rep.Error(h3.Pos, diag.CodeInvalidCombination,
			"http3 needs protocol https",
			"HTTP/3 always uses TLS; add `protocol https` to this listener")
	}
}

// validateSite checks a site as a whole: its routes, and that manual TLS has
// a certificate and key (and that they are not given for other TLS modes).
func validateSite(d *ast.Directive, rep Reporter) {
	if d.Block == nil {
		return
	}
	checkRoutesWith(rep, nil, childRoutes(d.Block))

	mode := ""
	var tlsDir *ast.Directive
	if t := child(d, "tls"); t != nil && len(t.Args) == 1 {
		tlsDir, mode = t, strDirective(t.Args[0])
	}
	cert, key := child(d, "certificate"), child(d, "certificate_key")
	if mode == "manual" {
		if cert == nil {
			rep.Error(tlsDir.Pos, diag.CodeMissingRequired, `tls manual needs a "certificate" directive`, `usage: certificate "/path/to/fullchain.pem"`)
		}
		if key == nil {
			rep.Error(tlsDir.Pos, diag.CodeMissingRequired, `tls manual needs a "certificate_key" directive`, `usage: certificate_key "/path/to/privkey.pem"`)
		}
		return
	}
	for _, c := range []*ast.Directive{cert, key} {
		if c != nil {
			rep.Error(c.Pos, diag.CodeInvalidCombination,
				fmt.Sprintf("%q is only used with tls manual", c.Name),
				"add `tls manual` to this site, or remove it")
		}
	}
}

func strDirective(v ast.Value) string {
	switch x := v.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.String:
		s, _ := x.Plain()
		return s
	}
	return ""
}
