package schema

import (
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/laikait/lotus/internal/config/ast"
	"github.com/laikait/lotus/internal/config/diag"
	"github.com/laikait/lotus/internal/htaccess"
	"github.com/laikait/lotus/internal/proxy"
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
	proxyDir := scalar("proxy", "upstream", String(), "none",
		"Sends matching requests to the named upstream (a reverse proxy). Takes the place of try_files and php for the route.",
		"The request, with its headers and body, goes to a server of that upstream. Only administrators may choose upstreams.")
	proxyDir.Args[0].Ref = "upstream"
	proxyDir.TenantForbidden = true
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
			Name: "upstream", Card: Keyed, Args: []Arg{{Name: "name", Type: String()}},
			Block: BlockRequired, Opens: CtxUpstream, Defines: "upstream", TenantForbidden: true,
			Since: since, Default: "none",
			Description: "A group of backend servers that routes send requests to with the proxy directive, with load balancing and health checks.",
			Security:    "Which servers a site can reach is an administrator decision: a route that proxies to an internal address can expose internal services.",
		},
		&Directive{
			Name: "site", Card: Keyed, Args: []Arg{{Name: "name", Type: Hostname()}},
			Block: BlockRequired, Opens: CtxSite, TenantForbidden: true,
			Since: since, Default: "none",
			Description: "A virtual host. The name is its primary hostname; aliases add more.",
			Security:    "Sites are separated by hostname only. LOTUS reads every site's static files as its own user, so isolate tenants with file permissions and the symlink policy.",
			Validate:    validateSite,
		},
		func() *Directive {
			d := container("metrics", CtxMetrics,
				"A separate listener that serves metrics in the Prometheus text format (requests, latency, connections, backends, certificates). Off unless this block is present.",
				"Metrics tell an attacker what sites exist and how busy they are. Keep the listener on a loopback or private address, and require a bearer_token or an allow list if it is reachable from a network.").withForbidden()
			d.Validate = validateMetrics
			return d
		}(),
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
		scalar("max_connections_per_ip", "count", IntAtLeast(1), "unlimited",
			"Maximum simultaneous connections from one client address (IPv6 clients are counted per /64). Connections beyond it are closed. Connections from trusted_proxies are not counted, since every client of such a proxy shares its address. Server-wide only.",
			"Without it one client can use every connection slot. Too low a value affects many users behind one NAT address."),
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
		scalar("storage", "path", Path(), "/var/lib/lotus/certs (Windows: %ProgramData%\\lotus\\certs)",
			"Directory for the ACME account key, certificates and their private keys. Created with access for the LOTUS user only.",
			"Everything in it is secret. Anyone who can read it can impersonate your sites. Back it up: lost certificates are re-issued, but CAs limit how often."),
		scalar("ca_root", "path", Path(), "none",
			"PEM file with the root certificate of a private or test CA (such as Pebble), trusted only for talking to the ACME server.",
			"Only for CAs you run yourself. It does not change which certificates browsers trust."),
		scalar("challenges", "types", List(Enum("http-01", "tls-alpn-01", "dns-01"), 1), `[ "http-01", "tls-alpn-01" ] (plus "dns-01" with a dns block)`,
			"Which validations LOTUS offers the CA. http-01 needs the CA to reach a plain-HTTP listener on port 80; tls-alpn-01 needs an https listener on port 443; dns-01 needs a dns block and is the only one that works for wildcard names. Use one when the other port is closed.",
			"None."),
		&Directive{
			Name: "dns", Card: Single, Block: BlockRequired, Opens: CtxDNS, TenantForbidden: true,
			Since: since, Default: "none",
			Description: "How LOTUS publishes the DNS record for a dns-01 validation. Enables dns-01 and wildcard certificates.",
			Security:    "The credentials can change the DNS of your domains. Give them the least access that works (one zone, DNS records only).",
		},
		scalar("http_port", "port", Int(1, 65535), "80",
			"Port the CA connects to for HTTP-01 validation, if it is not 80 (for example behind a port forward). A plain-HTTP listener must answer on it.",
			"None."),
		scalar("tls_alpn_port", "port", Int(1, 65535), "443",
			"Port the CA connects to for TLS-ALPN-01 validation, if it is not 443. An https listener must answer on it.",
			"None."),
		func() *Directive {
			d := scalar("eab_hmac_key", "key", String(), "none",
				"External account binding HMAC key, for CAs that require it.",
				"A credential: it is redacted in all output. Prefer env(\"NAME\") over writing it into the file.")
			d.Secret = true
			return d
		}(),
	)

	r.Define(CtxDNS,
		scalar("provider", "name", Enum("cloudflare", "exec"), "none",
			"cloudflare sets the record through the Cloudflare API. exec runs a program you provide with `present` or `cleanup` and the record in LOTUS_ACME_ZONE, LOTUS_ACME_NAME, LOTUS_ACME_VALUE and LOTUS_ACME_TTL (see docs/architecture/acme.md).",
			"exec runs with the privileges of the LOTUS user. Only the administrator configuration can name it."),
		func() *Directive {
			d := scalar("api_token", "token", String(), "none",
				"Cloudflare API token with Zone.DNS:Edit (and Zone:Read unless limited to one zone). Not a global API key.",
				"A credential: it is redacted in all output. Prefer env(\"NAME\") over writing it into the file.")
			d.Secret = true
			return d
		}(),
		scalar("command", "path", Path(), "none",
			"Program for the exec provider. Started without a shell.", "It runs as the LOTUS user; make it writable by administrators only."),
		scalar("propagation_delay", "duration", Duration(0), "0s",
			"Time to wait after the record is published before checking that it is visible.", "None."),
		scalar("propagation_timeout", "duration", Duration(0), "2m",
			"How long to wait for the record to become visible before the CA is asked to check. 0 uses the default.", "None."),
		scalar("propagation_check", "enabled", Bool(), "true",
			"Check, before asking the CA to validate, that the record can be seen in DNS. Turn it off when the name servers you can query don't show what the CA sees (split-horizon DNS).",
			"Off means the CA may be asked too early; the attempt then fails and is retried later."),
		scalar("resolvers", "servers", List(String(), 1), "the system's",
			"DNS servers (host:port) asked whether the record is visible and which zone a name belongs to.", "None."),
	).Required = []string{"provider"}

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
	htOn := scalar("htaccess", "enabled", Bool(), "false",
		"Reads .htaccess files in the document root and the directories below it, for the directives in htaccess_allow. Off by default.",
		"A .htaccess file is written by whoever can write to the site. It can rewrite, redirect and deny URLs of its own directory tree but is never executed as code, and it can't loosen the site's symlink, dotfile or security header settings.")
	htOn.TenantForbidden = true
	htAllow := scalar("htaccess_allow", "categories", List(Enum(htaccess.Categories...), 1), "all supported categories",
		"Which kinds of directives .htaccess files may use: "+strings.Join(htaccess.Categories, ", ")+". A directive of another kind is treated as unsupported.",
		"Leave out access to stop .htaccess from relaxing or adding access rules; leave out headers to stop it adding response headers.")
	htAllow.TenantForbidden = true
	htUnsupported := scalar("htaccess_unsupported", "action", Enum("error", "ignore"), "error",
		"What happens when a .htaccess file holds a directive LOTUS doesn't support or the site doesn't allow: error answers 500 and logs the directive; ignore skips it with a warning. Directives that grant or restrict access are never skipped.",
		"ignore can change what a site does without any visible failure. Use `lotus compat htaccess check` before relying on it.")
	htUnsupported.TenantForbidden = true
	htRecheck := scalar("htaccess_recheck", "interval", Duration(0), "2s",
		"How long a .htaccess file (or its absence) is remembered before the file system is looked at again. 0s checks on every request.",
		"Short intervals cost a file system lookup per directory per request.")
	htRecheck.TenantForbidden = true
	rateLimit := &Directive{
		Name: "rate_limit", Card: Single, Block: BlockRequired, Opens: CtxRateLimit, TenantForbidden: true,
		Since: since, Default: "none",
		Description: "Limits how many requests a client may make. Requests over the limit get 429 with Retry-After. In a site it counts every request of the site; in a route it adds a limit for that route (both apply).",
		Security:    "Counted per client address in memory, with a bounded table. Behind a proxy, list it in trusted_proxies or all clients share one allowance.",
	}
	htAuthFailures := scalar("htaccess_auth_failures", "count", Int(0, 1000), "10",
		"After this many wrong Basic authentication attempts from one client address within htaccess_auth_lockout, further attempts get 429 until the lock-out ends, without checking passwords. 0 turns the lock-out off.",
		"Without it a password can be guessed as fast as the server answers.")
	htAuthFailures.TenantForbidden = true
	htAuthLockout := scalar("htaccess_auth_lockout", "duration", Duration(time.Second), "5m",
		"The window in which wrong attempts are counted, and how long a client is locked out afterwards.", "A long lock-out also locks out a legitimate user behind the same address.")
	htAuthLockout.TenantForbidden = true
	siteLimits := *limits
	r.Define(CtxSite,
		listeners, aliases, siteTLS, certificate, certificateKey, symlinks, dotfiles, root, index, route, logs, &siteLimits, secHeaders,
		errorPage, htOn, htAllow, htUnsupported, htRecheck, htAuthFailures, htAuthLockout, rateLimit,
	)

	// ---- route ----

	routeIndex := *index
	routeRateLimit := *rateLimit
	routeLimits := *maxBody
	r.Define(CtxRoute,
		route, tryFiles, php, phpDeny, ret, rewrite, errorPage, &routeIndex, &routeLimits, &routeRateLimit,
		proxyDir,
		&Directive{
			Name: "proxy_set_header", Card: Repeated, Args: []Arg{{Name: "name", Type: String()}, {Name: "value", Type: String()}},
			Since: since, Default: "none",
			Description: "Sets a header on the request sent to the upstream. An empty value removes the header. Forwarding headers (X-Forwarded-For, -Host, -Proto, Forwarded) are set by LOTUS and can't be changed here.",
			Security:    "Headers such as X-Forwarded-User are trusted by many applications. Only set them to fixed values.",
			Validate: func(d *ast.Directive, rep Reporter) {
				if len(d.Args) != 2 {
					return
				}
				name, _ := plainString(d.Args[0])
				if !headerNameRe.MatchString(name) {
					rep.Error(d.Args[0].Position(), diag.CodeInvalidValue, fmt.Sprintf("%q is not a valid header name", name), "")
				} else if proxyOwnedHeader(name) {
					rep.Error(d.Args[0].Position(), diag.CodeInvalidValue, fmt.Sprintf("%s is set by LOTUS and can't be changed with proxy_set_header", name), "")
				}
				if v, ok := plainString(d.Args[1]); ok && !validHeaderValue(v) {
					rep.Error(d.Args[1].Position(), diag.CodeInvalidValue, "header values must not contain control characters", "")
				}
			},
		},
		scalar("proxy_host", "host", Enum("client", "upstream"), "client",
			"Which Host header the upstream gets: client keeps the one the client sent (and the application sees its own public name); upstream uses the backend's own address.",
			"None."),
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
		ret, tryFiles, php, phpDeny, proxyDir,
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

	// ---- rate_limit ----

	rl := r.Define(CtxRateLimit,
		scalar("requests", "count", IntAtLeast(1), "none",
			"How many requests are allowed per period (see per).", "Set it above what a real visitor does, so only abusive clients reach it."),
		scalar("per", "duration", Duration(time.Millisecond), "1s", "The period requests counts over.", "None."),
		scalar("burst", "count", IntAtLeast(1), "requests",
			"How many requests may arrive at once after a quiet time. The allowance refills continuously at requests per period.",
			"A small burst smooths traffic but refuses pages that load many files at once."),
		scalar("key", "key", Enum("ip", "global"), "ip",
			"What is counted separately: ip gives each client address its own allowance (IPv6 per /64, after trusted_proxies); global shares one allowance among all clients.",
			"With ip behind a proxy that is not in trusted_proxies, every client counts as the proxy and shares one allowance."),
	)
	rl.Required = []string{"requests"}

	// ---- upstream ----

	backendAddr := func(d *ast.Directive, rep Reporter) {
		if len(d.Args) != 1 {
			return
		}
		s, ok := plainString(d.Args[0])
		if !ok {
			return
		}
		if _, err := proxy.ParseBackend(s); err != nil {
			rep.Error(d.Args[0].Position(), diag.CodeInvalidValue, err.Error(),
				`examples: backend "http://10.0.0.5:8080"   backend "https://api.internal:443"   backend "unix:/run/app.sock"`)
		}
	}
	up := r.Define(CtxUpstream,
		&Directive{
			Name: "backend", Card: Keyed, Args: []Arg{{Name: "address", Type: String()}},
			Block: BlockOptional, Opens: CtxBackend, Since: since, Default: "none", Validate: backendAddr,
			Description: "A server that requests are sent to: http://host:port, https://host:port or unix:/path. A block can give it a weight and a connection limit.",
			Security:    "Requests are sent to this address as they are. Use https:// across networks you don't control, and keep tls_verify on.",
		},
		scalar("balance", "method", Enum("round_robin", "least_conn", "random", "ip_hash", "uri_hash"), "round_robin",
			"How requests are spread over the backends. round_robin follows the weights; least_conn picks the backend with the fewest active requests; random picks the less loaded of two random backends; ip_hash and uri_hash send the same client address, or the same path, to the same backend.",
			"ip_hash and uri_hash don't balance load evenly if a few clients or paths are very busy."),
		scalar("connect_timeout", "duration", Duration(time.Millisecond), "5s",
			"Time allowed to connect to a backend (and finish its TLS handshake).",
			"A long timeout keeps requests waiting on a backend that is down."),
		scalar("response_timeout", "duration", Duration(time.Millisecond), "60s",
			"Time to wait for a backend's response headers after the request was sent. A request that times out is not repeated.",
			"Long timeouts let slow backends tie up connections."),
		scalar("idle_timeout", "duration", Duration(time.Second), "90s",
			"How long an unused connection to a backend is kept for reuse.", "None."),
		scalar("retries", "count", Int(0, 10), "1",
			"How many other backends a request may be tried on after a failure. Only requests that are safe to repeat are: those that never connected, and GET, HEAD, OPTIONS, PUT and DELETE without a body that failed before a response.",
			"A POST that reached a backend is never repeated, so this can't make a payment twice."),
		scalar("max_fails", "count", IntAtLeast(0), "3",
			"After this many failures within fail_timeout a backend is taken out of rotation for fail_timeout. 0 turns passive checking off.",
			"None."),
		scalar("fail_timeout", "duration", Duration(time.Second), "10s",
			"The window in which max_fails failures are counted, and how long a backend stays out afterwards.", "None."),
		scalar("max_conns", "count", IntAtLeast(1), "unlimited",
			"Most requests in progress across the whole upstream. Beyond it requests get 503 at once.",
			"Protects the backends from a flood; set it to what they can serve."),
		&Directive{
			Name: "health_check", Card: Single, Block: BlockRequired, Opens: CtxHealthCheck, Since: since, Default: "none",
			Description: "Probes every backend regularly and takes failing ones out of rotation until they recover. Without a path it only tries to connect.",
			Security:    "A health path is requested without credentials. Don't point it at something that changes data.",
		},
		scalar("tls_verify", "enabled", Bool(), "true",
			"Check the certificate of https backends against the trusted authorities and the backend's name.",
			"false lets anyone between LOTUS and the backend read and change the traffic. Use tls_ca for a private authority instead."),
		scalar("tls_ca", "path", Path(), "system certificates",
			"PEM file of the authority that signed the backends' certificates. It replaces the system list for this upstream.", "None."),
		scalar("tls_server_name", "name", String(), "the backend's host",
			"Name to expect in the backend's certificate and to send as SNI.", "None."),
		scalar("tls_client_cert", "path", Path(), "none",
			"Certificate that LOTUS presents to https backends that require client certificates (with tls_client_key).",
			"The key file must be readable only by the LOTUS user."),
		scalar("tls_client_key", "path", Path(), "none", "Private key for tls_client_cert.", "Keep the file private."),
	)
	up.Required = []string{"backend"}
	up.Check = func(present map[string]bool) (string, string, string) {
		if present["tls_client_cert"] != present["tls_client_key"] {
			return diag.CodeMissingRequired, "tls_client_cert and tls_client_key go together", "give both or neither"
		}
		return "", "", ""
	}
	r.Define(CtxBackend,
		scalar("weight", "weight", Int(1, 1000), "1", "Share of requests relative to the other backends (round_robin, ip_hash, uri_hash, random and least_conn).", "None."),
		scalar("max_conns", "count", IntAtLeast(1), "unlimited", "Most requests in progress on this backend. Requests go to other backends while it is full.", "None."),
	)
	r.Define(CtxHealthCheck,
		scalar("path", "path", String(), "none (connect only)",
			"URL path requested with GET. A status from 200 to 399 is healthy.", "None."),
		scalar("interval", "duration", Duration(100*time.Millisecond), "10s", "Time between probes of a backend.", "Short intervals add load to the backends."),
		scalar("timeout", "duration", Duration(10*time.Millisecond), "3s", "Time a probe may take.", "None."),
		scalar("healthy_threshold", "count", Int(1, 20), "2", "Successful probes in a row that bring a backend back.", "None."),
		scalar("unhealthy_threshold", "count", Int(1, 20), "3", "Failed probes in a row that take a backend out.", "None."),
	)

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

	// ---- metrics ----

	r.Define(CtxMetrics,
		scalar("address", "address", Address(HostPort), "none",
			"Address to listen on, as host:port. Use 127.0.0.1 unless a collector on another machine must reach it.",
			"A non-loopback address needs bearer_token or allow."),
		scalar("path", "path", String(), "/metrics",
			"URL path that serves the metrics. Any other path is 404.", "None."),
		func() *Directive {
			d := scalar("bearer_token", "token", String(), "none",
				"If set, a request must carry `Authorization: Bearer TOKEN`. Compared in constant time.",
				"A credential: it is redacted in all output. Prefer env(\"NAME\") over writing it into the file.")
			d.Secret = true
			return d
		}(),
		&Directive{
			Name: "allow", Card: Single, Args: []Arg{{Name: "networks", Type: List(CIDR(), 1)}}, TenantForbidden: true,
			Since: since, Default: "any",
			Description: "If set, only clients in these networks may read the metrics; others get 403. Checked against the connection's address (not X-Forwarded-For).",
			Security:    "Without bearer_token or allow, anyone who can reach the address can read the metrics.",
		},
	).Required = []string{"address"}

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

// validateMetrics checks a metrics block: the path is a URL path, and a listener
// that isn't on a loopback address is protected by a token or an allow list.
func validateMetrics(d *ast.Directive, rep Reporter) {
	if p := child(d, "path"); p != nil && len(p.Args) == 1 {
		path := strDirective(p.Args[0])
		if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, " ?#\t\r\n") {
			rep.Error(p.Pos, diag.CodeInvalidValue, "the metrics path must start with / and contain no space, ? or #", "for example /metrics")
		}
	}
	a := child(d, "address")
	if a == nil || len(a.Args) != 1 {
		return
	}
	host, _, err := net.SplitHostPort(strDirective(a.Args[0]))
	if err != nil {
		return // the type check reports it
	}
	if ip, perr := netip.ParseAddr(host); host == "localhost" || (perr == nil && ip.IsLoopback()) {
		return
	}
	if child(d, "bearer_token") == nil && child(d, "allow") == nil {
		rep.Error(a.Pos, diag.CodeInvalidCombination,
			"the metrics listener is reachable from the network but nothing protects it",
			"add bearer_token or allow, or listen on 127.0.0.1")
	}
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

// proxyOwnedHeader says whether LOTUS sets a header itself on proxied requests.
func proxyOwnedHeader(name string) bool {
	switch strings.ToLower(name) {
	case "x-forwarded-for", "x-forwarded-host", "x-forwarded-proto", "x-forwarded-port", "forwarded", "x-real-ip",
		"host", "connection", "upgrade", "transfer-encoding", "content-length", "te", "trailer", "keep-alive", "proxy-authorization", "proxy-connection":
		return true
	}
	return false
}
