// Package compiler turns a validated configuration tree into the typed
// model.Config the server runs on. It applies defaults, merges inherited
// settings (server, site, route), compiles patterns, and performs the checks
// that need a view of the whole configuration, such as duplicate hostnames.
//
// The input must come from the loader and have passed the validator, so
// argument types are already known to be right.
package compiler

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/laikait/lotus/internal/config/ast"
	"github.com/laikait/lotus/internal/config/diag"
	"github.com/laikait/lotus/internal/config/loader"
	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/htaccess"
)

// Compile builds the typed configuration. The returned Config is nil if any
// error was found.
func Compile(res *loader.Result) (*model.Config, []diag.Diagnostic) {
	c := &compiler{diags: diag.NewList(diag.DefaultMaxErrors), base: baseDir(res.File.Path)}
	cfg := &model.Config{
		Version: res.Version,
		Limits:  model.DefaultLimits(),
		TLS:     model.TLS{MinVersion: model.DefaultTLSMinVersion},
	}
	if server := find(res.File.Statements, "server"); server != nil && server.Block != nil {
		c.server(cfg, server)
	}
	c.crossChecks(cfg)
	if c.diags.HasErrors() {
		return nil, c.diags.Items()
	}
	return cfg, c.diags.Items()
}

// claim records where something that must be unique was declared.
type claim struct {
	key   string // the address or hostname
	owner string // listener or site name, for messages
	idx   int    // index of the owning site
	pos   diag.Pos
}

type compiler struct {
	// base is the directory of the root configuration file. Relative file paths
	// in directives are resolved against it (grammar §6.8), not against the
	// working directory, so the result does not depend on where lotus is started.
	base      string
	diags     *diag.List
	listeners []claim
	hosts     []claim
	sitePos   []diag.Pos // position of each site's name
}

// ---- tree helpers ----

func find(stmts []ast.Statement, name string) *ast.Directive {
	for _, s := range stmts {
		if d, ok := s.(*ast.Directive); ok && d.Name == name {
			return d
		}
	}
	return nil
}

func findAll(stmts []ast.Statement, name string) []*ast.Directive {
	var out []*ast.Directive
	for _, s := range stmts {
		if d, ok := s.(*ast.Directive); ok && d.Name == name {
			out = append(out, d)
		}
	}
	return out
}

func body(d *ast.Directive) []ast.Statement {
	if d == nil || d.Block == nil {
		return nil
	}
	return d.Block.Statements
}

// arg returns argument i, or nil.
func arg(d *ast.Directive, i int) ast.Value {
	if i < len(d.Args) {
		return d.Args[i]
	}
	return nil
}

func str(v ast.Value) string {
	switch x := v.(type) {
	case *ast.String:
		s, _ := x.Plain()
		return s
	case *ast.Ident:
		return x.Name
	}
	return ""
}

// tmpl returns a string that will be expanded at request time. Text that must
// stay literal (\{ and values from env() or file()) gets a
// model.LiteralBrace marker before each '{', so it is never mistaken for a placeholder.
func tmpl(v ast.Value) string {
	s, ok := v.(*ast.String)
	if !ok {
		return str(v)
	}
	var b strings.Builder
	for _, p := range s.Parts {
		if p.Escaped {
			b.WriteString(strings.ReplaceAll(p.Text, "{", model.LiteralBrace+"{"))
		} else {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func tmpls(v ast.Value) []string {
	l, ok := v.(*ast.List)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(l.Items))
	for _, it := range l.Items {
		out = append(out, tmpl(it))
	}
	return out
}

func strs(v ast.Value) []string {
	l, ok := v.(*ast.List)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(l.Items))
	for _, it := range l.Items {
		out = append(out, str(it))
	}
	return out
}

func integer(v ast.Value) int64 {
	if n, ok := v.(*ast.Int); ok {
		return n.Value
	}
	return 0
}

func boolean(v ast.Value) bool {
	b, ok := v.(*ast.Bool)
	return ok && b.Value
}

func size(v ast.Value) int64 {
	if s, ok := v.(*ast.Size); ok {
		return s.Bytes
	}
	return 0
}

func dur(v ast.Value) time.Duration {
	if d, ok := v.(*ast.Duration); ok {
		return d.Value
	}
	return 0
}

func isNull(v ast.Value) bool { _, ok := v.(*ast.Null); return ok }

// operatorAndRest splits an optional leading operator from the arguments.
func operatorAndRest(args []ast.Value) (op string, rest []ast.Value) {
	if len(args) > 0 {
		if o, ok := args[0].(*ast.Operator); ok {
			return o.Op, args[1:]
		}
	}
	return "", args
}

func prefixes(v ast.Value) []netip.Prefix {
	var out []netip.Prefix
	for _, s := range strs(v) {
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
		} else if a, err := netip.ParseAddr(s); err == nil {
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	return out
}

// ---- server ----

func (c *compiler) server(cfg *model.Config, server *ast.Directive) {
	for _, st := range body(server) {
		d, ok := st.(*ast.Directive)
		if !ok {
			continue
		}
		switch d.Name {
		case "runtime":
			for _, s := range body(d) {
				if r, ok := s.(*ast.Directive); ok {
					switch r.Name {
					case "max_procs":
						cfg.Runtime.MaxProcs = int(integer(arg(r, 0))) // "auto" is not an Int: stays 0
					case "memory_limit":
						cfg.Runtime.MemoryLimit = size(arg(r, 0))
					}
				}
			}
		case "listen":
			cfg.Listeners = append(cfg.Listeners, c.listener(d))
		case "trusted_proxies":
			cfg.TrustedProxies = prefixes(arg(d, 0))
		case "limits":
			cfg.Limits = applyLimits(cfg.Limits, d)
		case "tls":
			cfg.TLS = c.tlsSettings(cfg.TLS, d)
		case "php_backend":
			cfg.PHPBackends = append(cfg.PHPBackends, c.phpBackend(d))
		case "upstream":
			cfg.Upstreams = append(cfg.Upstreams, c.upstream(d))
		case "cache":
			cfg.Caches = append(cfg.Caches, c.cacheZone(d))
		case "metrics":
			m := &model.Metrics{Path: "/metrics"}
			for _, s := range body(d) {
				if r, ok := s.(*ast.Directive); ok {
					switch r.Name {
					case "address":
						m.Address = str(arg(r, 0))
					case "path":
						m.Path = str(arg(r, 0))
					case "bearer_token":
						m.BearerToken = model.Secret(str(arg(r, 0)))
					case "allow":
						m.Allow = prefixes(arg(r, 0))
					}
				}
			}
			cfg.Metrics = m
		case "control":
			for _, s := range body(d) {
				if r, ok := s.(*ast.Directive); ok {
					switch r.Name {
					case "socket":
						cfg.Control.Socket = c.path(str(arg(r, 0)))
					case "viewer_group":
						cfg.Control.ViewerGroup = str(arg(r, 0))
					}
				}
			}
		}
	}
	// Sites are compiled after everything else so they can inherit server settings.
	for _, d := range findAll(body(server), "site") {
		cfg.Sites = append(cfg.Sites, c.site(cfg, d))
	}
}

func (c *compiler) listener(d *ast.Directive) model.Listener {
	l := model.Listener{Name: str(arg(d, 0))}
	for _, s := range body(d) {
		if r, ok := s.(*ast.Directive); ok {
			switch r.Name {
			case "address":
				l.Address = str(arg(r, 0))
				c.listeners = append(c.listeners, claim{key: l.Address, owner: l.Name, pos: arg(r, 0).Position()})
			case "protocol":
				l.HTTPS = str(arg(r, 0)) == "https"
			case "http3":
				l.HTTP3 = boolean(arg(r, 0))
			}
		}
	}
	return l
}

// applyLimits overlays the limits in d onto base.
func applyLimits(base model.Limits, d *ast.Directive) model.Limits {
	for _, s := range body(d) {
		r, ok := s.(*ast.Directive)
		if !ok {
			continue
		}
		switch r.Name {
		case "max_connections":
			base.MaxConnections = int(integer(arg(r, 0)))
		case "max_connections_per_ip":
			base.MaxConnectionsPerIP = int(integer(arg(r, 0)))
		case "max_request_body":
			base.MaxRequestBody = size(arg(r, 0))
		case "max_header_bytes":
			base.MaxHeaderBytes = size(arg(r, 0))
		case "header_timeout":
			base.HeaderTimeout = model.Duration(dur(arg(r, 0)))
		case "read_timeout":
			base.ReadTimeout = model.Duration(dur(arg(r, 0)))
		case "write_timeout":
			base.WriteTimeout = model.Duration(dur(arg(r, 0)))
		case "idle_timeout":
			base.IdleTimeout = model.Duration(dur(arg(r, 0)))
		}
	}
	return base
}

func (c *compiler) tlsSettings(base model.TLS, d *ast.Directive) model.TLS {
	for _, s := range body(d) {
		r, ok := s.(*ast.Directive)
		if !ok {
			continue
		}
		switch r.Name {
		case "min_version":
			base.MinVersion = str(arg(r, 0))
		case "acme":
			a := &model.ACME{}
			for _, as := range body(r) {
				if f, ok := as.(*ast.Directive); ok {
					switch f.Name {
					case "email":
						a.Email = str(arg(f, 0))
					case "ca":
						a.CA = str(arg(f, 0))
					case "eab_key_id":
						a.EABKeyID = str(arg(f, 0))
					case "eab_hmac_key":
						a.EABHMACKey = model.Secret(str(arg(f, 0)))
					case "storage":
						a.Storage = c.path(str(arg(f, 0)))
					case "ca_root":
						a.CARoot = c.path(str(arg(f, 0)))
					case "challenges":
						a.Challenges = strs(arg(f, 0))
					case "http_port":
						a.HTTPPort = int(integer(arg(f, 0)))
					case "tls_alpn_port":
						a.TLSALPNPort = int(integer(arg(f, 0)))
					case "dns":
						a.DNS = c.dnsChallenge(f)
					}
				}
			}
			if a.Storage == "" {
				a.Storage = defaultACMEStorage()
			}
			c.checkChallenges(r, a)
			base.ACME = a
		}
	}
	return base
}

// dnsChallenge reads a dns block.
func (c *compiler) dnsChallenge(d *ast.Directive) *model.DNSChallenge {
	n := &model.DNSChallenge{}
	for _, s := range body(d) {
		f, ok := s.(*ast.Directive)
		if !ok {
			continue
		}
		switch f.Name {
		case "provider":
			n.Provider = str(arg(f, 0))
		case "api_token":
			n.APIToken = model.Secret(str(arg(f, 0)))
		case "command":
			cmd := str(arg(f, 0))
			if filepath.Base(cmd) == cmd {
				n.Command = cmd
			} else {
				n.Command = c.path(cmd)
			}
		case "propagation_delay":
			n.PropagationDelay = model.Duration(dur(arg(f, 0)))
		case "propagation_timeout":
			n.PropagationTimeout = model.Duration(dur(arg(f, 0)))
		case "propagation_check":
			n.SkipPropagationCheck = !boolean(arg(f, 0))
		case "resolvers":
			n.Resolvers = strs(arg(f, 0))
		}
	}
	return n
}

// dnsEnabled says whether dns-01 will be offered to the CA.
func dnsEnabled(a *model.ACME) bool {
	return a != nil && a.DNS != nil && (len(a.Challenges) == 0 || slices.Contains(a.Challenges, "dns-01"))
}

// checkChallenges rejects settings that contradict each other.
func (c *compiler) checkChallenges(d *ast.Directive, a *model.ACME) {
	asked := slices.Contains(a.Challenges, "dns-01")
	switch {
	case asked && a.DNS == nil:
		c.diags.ErrorHint(d.Pos, diag.CodeInvalidValue, "challenges lists dns-01 but there is no dns block",
			"add `dns { provider ... }` to the acme block, or remove dns-01")
	case a.DNS != nil && len(a.Challenges) > 0 && !asked:
		c.diags.ErrorHint(d.Pos, diag.CodeInvalidValue, "there is a dns block but challenges does not list dns-01",
			"add \"dns-01\" to challenges, or remove the dns block")
	}
	if n := a.DNS; n != nil {
		switch {
		case n.Provider == "cloudflare" && n.APIToken == "":
			c.diags.ErrorHint(d.Pos, diag.CodeInvalidValue, "the cloudflare dns provider needs an api_token", "api_token env(\"CLOUDFLARE_API_TOKEN\")")
		case n.Provider == "cloudflare" && n.Command != "":
			c.diags.Error(d.Pos, diag.CodeInvalidValue, "command belongs to the exec dns provider, not cloudflare")
		case n.Provider == "exec" && n.Command == "":
			c.diags.Error(d.Pos, diag.CodeInvalidValue, "the exec dns provider needs a command")
		case n.Provider == "exec" && n.APIToken != "":
			c.diags.Error(d.Pos, diag.CodeInvalidValue, "api_token belongs to the cloudflare dns provider, not exec")
		}
	}
}

func (c *compiler) phpBackend(d *ast.Directive) model.PHPBackend {
	b := model.PHPBackend{Name: str(arg(d, 0)), Timeout: model.Duration(model.DefaultPHPTimeout)}
	processes, maxRequests := model.DefaultPHPProcesses, model.DefaultPHPMaxRequests
	for _, s := range body(d) {
		if r, ok := s.(*ast.Directive); ok {
			switch r.Name {
			case "fastcgi_pass":
				addr := str(arg(r, 0))
				if rest, isUnix := strings.CutPrefix(addr, "unix:"); isUnix {
					b.Network, b.Address = "unix", c.path(rest)
				} else {
					b.Network, b.Address = "tcp", addr
				}
			case "timeout":
				b.Timeout = model.Duration(dur(arg(r, 0)))
			case "command":
				// A bare program name is looked up in PATH; anything with a
				// directory is relative to the configuration file.
				cmd := str(arg(r, 0))
				if filepath.Base(cmd) == cmd {
					b.Command = cmd
				} else {
					b.Command = c.path(cmd)
				}
			case "processes":
				processes = int(integer(arg(r, 0)))
			case "max_requests":
				maxRequests = int(integer(arg(r, 0)))
			case "php_ini":
				b.PHPIni = c.path(str(arg(r, 0)))
			}
		}
	}
	if b.Command != "" {
		b.Processes, b.MaxRequests = processes, maxRequests
	}
	return b
}

// rateLimit compiles a rate_limit block.
func rateLimit(d *ast.Directive) *model.RateLimit {
	rl := &model.RateLimit{Per: model.Duration(time.Second), Key: model.RateKeyIP}
	for _, s := range body(d) {
		if x, ok := s.(*ast.Directive); ok {
			switch x.Name {
			case "requests":
				rl.Requests = int(integer(arg(x, 0)))
			case "per":
				rl.Per = model.Duration(dur(arg(x, 0)))
			case "burst":
				rl.Burst = int(integer(arg(x, 0)))
			case "key":
				rl.Key = str(arg(x, 0))
			}
		}
	}
	if rl.Burst == 0 {
		rl.Burst = rl.Requests
	}
	return rl
}

func (c *compiler) cacheZone(d *ast.Directive) model.CacheZone {
	z := model.CacheZone{Name: str(arg(d, 0)), MaxSize: model.DefaultCacheMaxSize, MaxEntrySize: model.DefaultCacheMaxEntrySize}
	entrySet := false
	for _, s := range body(d) {
		if r, ok := s.(*ast.Directive); ok {
			switch r.Name {
			case "max_size":
				z.MaxSize = size(arg(r, 0))
			case "max_entry_size":
				z.MaxEntrySize, entrySet = size(arg(r, 0)), true
			}
		}
	}
	// A zone smaller than the default entry size just caps it; an entry size
	// the administrator wrote that can't fit is a mistake.
	if z.MaxEntrySize > z.MaxSize {
		if entrySet {
			c.diags.ErrorHint(d.Pos, diag.CodeInvalidValue,
				fmt.Sprintf("cache %q: max_entry_size (%d bytes) is larger than max_size (%d bytes)", z.Name, z.MaxEntrySize, z.MaxSize),
				"no entry could ever be stored; raise max_size or lower max_entry_size")
		}
		z.MaxEntrySize = z.MaxSize
	}
	return z
}

func (c *compiler) routeCache(d *ast.Directive) *model.RouteCache {
	rc := &model.RouteCache{
		Zone: str(arg(d, 0)), LockTimeout: model.Duration(model.DefaultCacheLockTimeout), Cookies: model.CookiesBypassAny,
	}
	for _, s := range body(d) {
		r, ok := s.(*ast.Directive)
		if !ok {
			continue
		}
		switch r.Name {
		case "ttl":
			rc.TTL = model.Duration(dur(arg(r, 0)))
		case "negative_ttl":
			rc.NegativeTTL = model.Duration(dur(arg(r, 0)))
		case "stale_if_error":
			rc.StaleIfError = model.Duration(dur(arg(r, 0)))
		case "lock_timeout":
			rc.LockTimeout = model.Duration(dur(arg(r, 0)))
		case "bypass_cookies":
			switch v := arg(r, 0).(type) {
			case *ast.List:
				rc.Cookies, rc.BypassCookies = model.CookiesBypassNamed, strs(v)
			default:
				rc.Cookies = str(v)
			}
		}
	}
	return rc
}

func (c *compiler) upstream(d *ast.Directive) model.Upstream {
	u := model.Upstream{
		Name: str(arg(d, 0)), Balance: "round_robin", TLSVerify: true,
		ConnectTimeout:  model.Duration(model.DefaultUpstreamConnectTimeout),
		ResponseTimeout: model.Duration(model.DefaultUpstreamResponseTimeout),
		IdleTimeout:     model.Duration(model.DefaultUpstreamIdleTimeout),
		Retries:         model.DefaultUpstreamRetries,
		MaxFails:        model.DefaultUpstreamMaxFails,
		FailTimeout:     model.Duration(model.DefaultUpstreamFailTimeout),
	}
	for _, s := range body(d) {
		r, ok := s.(*ast.Directive)
		if !ok {
			continue
		}
		switch r.Name {
		case "backend":
			b := model.UpstreamBackend{Address: str(arg(r, 0)), Weight: 1}
			for _, bs := range body(r) {
				if bd, ok := bs.(*ast.Directive); ok {
					switch bd.Name {
					case "weight":
						b.Weight = int(integer(arg(bd, 0)))
					case "max_conns":
						b.MaxConns = int(integer(arg(bd, 0)))
					}
				}
			}
			u.Backends = append(u.Backends, b)
		case "balance":
			u.Balance = str(arg(r, 0))
		case "connect_timeout":
			u.ConnectTimeout = model.Duration(dur(arg(r, 0)))
		case "response_timeout":
			u.ResponseTimeout = model.Duration(dur(arg(r, 0)))
		case "idle_timeout":
			u.IdleTimeout = model.Duration(dur(arg(r, 0)))
		case "retries":
			u.Retries = int(integer(arg(r, 0)))
		case "max_fails":
			u.MaxFails = int(integer(arg(r, 0)))
		case "fail_timeout":
			u.FailTimeout = model.Duration(dur(arg(r, 0)))
		case "max_conns":
			u.MaxConns = int(integer(arg(r, 0)))
		case "tls_verify":
			u.TLSVerify = boolean(arg(r, 0))
		case "tls_ca":
			u.TLSCA = c.path(str(arg(r, 0)))
		case "tls_server_name":
			u.TLSServerName = str(arg(r, 0))
		case "tls_client_cert":
			u.TLSClientCert = c.path(str(arg(r, 0)))
		case "tls_client_key":
			u.TLSClientKey = c.path(str(arg(r, 0)))
		case "health_check":
			h := &model.HealthCheck{Interval: model.Duration(10 * time.Second), Timeout: model.Duration(3 * time.Second), HealthyThreshold: 2, UnhealthyThreshold: 3}
			for _, hs := range body(r) {
				if hd, ok := hs.(*ast.Directive); ok {
					switch hd.Name {
					case "path":
						h.Path = str(arg(hd, 0))
					case "interval":
						h.Interval = model.Duration(dur(arg(hd, 0)))
					case "timeout":
						h.Timeout = model.Duration(dur(arg(hd, 0)))
					case "healthy_threshold":
						h.HealthyThreshold = int(integer(arg(hd, 0)))
					case "unhealthy_threshold":
						h.UnhealthyThreshold = int(integer(arg(hd, 0)))
					}
				}
			}
			u.Health = h
		}
	}
	return u
}

// ---- sites ----

type inherited struct {
	root       string
	index      []string
	maxBody    int64
	errorPages map[int]string
}

// errorPages returns the error pages in effect inside a block: the inherited
// ones, with those the block defines itself replacing them one by one. Two
// definitions for the same status in one block are an error.
func (c *compiler) errorPages(stmts []ast.Statement, parent map[int]string) map[int]string {
	out := map[int]string{}
	for status, target := range parent {
		out[status] = target
	}
	own := map[int]bool{}
	for _, st := range stmts {
		d, ok := st.(*ast.Directive)
		if !ok || d.Name != "error_page" {
			continue
		}
		status := int(integer(arg(d, 0)))
		if own[status] {
			c.diags.Error(d.Pos, diag.CodeRouteDuplicate,
				fmt.Sprintf("duplicate error_page for status %d (each status can have one error page per block)", status))
			continue
		}
		own[status] = true
		out[status] = str(arg(d, 1))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (c *compiler) site(cfg *model.Config, d *ast.Directive) model.Site {
	s := model.Site{
		Name:     strings.ToLower(str(arg(d, 0))),
		Index:    []string{"index.html"},
		Limits:   cfg.Limits,
		Symlinks: model.SymlinksOwnerMatch,
		Dotfiles: model.DotfilesDeny,
	}
	idx := len(cfg.Sites)
	c.sitePos = append(c.sitePos, arg(d, 0).Position())
	c.hosts = append(c.hosts, claim{key: s.Name, owner: s.Name, idx: idx, pos: arg(d, 0).Position()})
	sawListeners := false
	var ht *model.HTAccess
	htOn := false
	for _, st := range body(d) {
		r, ok := st.(*ast.Directive)
		if !ok {
			continue
		}
		switch r.Name {
		case "rate_limit":
			s.RateLimit = rateLimit(r)
		case "htaccess", "htaccess_allow", "htaccess_unsupported", "htaccess_recheck", "htaccess_auth_failures", "htaccess_auth_lockout":
			if ht == nil {
				ht = &model.HTAccess{Allow: append([]string(nil), htaccess.Categories...),
					OnUnsupported: model.HTAccessError, Recheck: model.Duration(model.DefaultHTAccessRecheck),
					AuthFailures: model.DefaultHTAccessAuthFailures, AuthLockout: model.Duration(model.DefaultHTAccessAuthLockout)}
			}
			switch r.Name {
			case "htaccess":
				htOn = boolean(arg(r, 0))
			case "htaccess_allow":
				ht.Allow = strs(arg(r, 0))
			case "htaccess_unsupported":
				ht.OnUnsupported = str(arg(r, 0))
			case "htaccess_recheck":
				ht.Recheck = model.Duration(dur(arg(r, 0)))
			case "htaccess_auth_failures":
				ht.AuthFailures = int(integer(arg(r, 0)))
			case "htaccess_auth_lockout":
				ht.AuthLockout = model.Duration(dur(arg(r, 0)))
			}
		case "listeners":
			s.Listeners, sawListeners = strs(arg(r, 0)), true
		case "aliases":
			if l, ok := arg(r, 0).(*ast.List); ok {
				for _, item := range l.Items {
					a := strings.ToLower(str(item))
					s.Aliases = append(s.Aliases, a)
					c.hosts = append(c.hosts, claim{key: a, owner: s.Name, idx: idx, pos: item.Position()})
				}
			}
		case "tls":
			s.TLS = str(arg(r, 0))
		case "certificate":
			s.Certificate = c.path(str(arg(r, 0)))
		case "certificate_key":
			s.CertificateKey = c.path(str(arg(r, 0)))
		case "symlinks":
			s.Symlinks = str(arg(r, 0))
		case "dotfiles":
			s.Dotfiles = str(arg(r, 0))
		case "root":
			s.Root = c.path(str(arg(r, 0)))
		case "index":
			s.Index = strs(arg(r, 0))
		case "limits":
			s.Limits = applyLimits(s.Limits, r)
		case "logs":
			s.Logs = c.logs(r)
		case "security_headers":
			for _, ps := range body(r) {
				if p, ok := ps.(*ast.Pair); ok {
					s.SecurityHeaders = append(s.SecurityHeaders, model.Header{Name: str(p.Key), Value: str(p.Value)})
				}
			}
		}
	}
	if htOn {
		s.HTAccess = ht
	}
	if !sawListeners {
		for _, l := range cfg.Listeners {
			s.Listeners = append(s.Listeners, l.Name)
		}
	}
	if s.TLS == "" {
		s.TLS = model.TLSOff
		for _, name := range s.Listeners {
			for _, l := range cfg.Listeners {
				if l.Name == name && l.HTTPS {
					s.TLS = model.TLSAutomatic
				}
			}
		}
	}

	s.ErrorPages = c.errorPages(body(d), nil)
	in := inherited{root: s.Root, index: s.Index, maxBody: s.Limits.MaxRequestBody, errorPages: s.ErrorPages}
	for _, rd := range findAll(body(d), "route") {
		route, ok := c.route(rd, in)
		if !ok {
			continue
		}
		if route.Kind == model.RouteNamed {
			s.Named = append(s.Named, route)
		} else {
			s.Routes = append(s.Routes, route)
		}
	}
	return s
}

func (c *compiler) logs(d *ast.Directive) model.Logs {
	var l model.Logs
	target := func(v ast.Value) model.LogTarget {
		if isNull(v) {
			return model.LogTarget{Off: true}
		}
		return model.LogTarget{Path: c.path(str(v))}
	}
	for _, s := range body(d) {
		if r, ok := s.(*ast.Directive); ok {
			switch r.Name {
			case "access":
				l.Access = target(arg(r, 0))
			case "error":
				l.Error = target(arg(r, 0))
			}
		}
	}
	return l
}

// ---- routes ----

func (c *compiler) route(d *ast.Directive, in inherited) (model.Route, bool) {
	op, rest := operatorAndRest(d.Args)
	if len(rest) != 1 {
		return model.Route{}, false
	}
	path := str(rest[0])
	r := model.Route{Path: path}
	switch op {
	case "":
		r.Kind = model.RoutePrefix
		if strings.HasPrefix(path, "@") {
			r.Kind = model.RouteNamed
		}
	case "=":
		r.Kind = model.RouteExact
	case "^~":
		r.Kind = model.RouteFinalPrefix
	case "~", "~*":
		r.Kind = model.RouteRegex
		pattern := path
		if op == "~*" {
			r.Kind, pattern = model.RouteRegexFold, "(?i)"+path
		}
		re, err := regexp.Compile(pattern)
		if err != nil { // the validator checked this; keep the compiler honest
			c.diags.Error(rest[0].Position(), diag.CodeInvalidRegex, "invalid regular expression")
			return model.Route{}, false
		}
		r.Regexp = re
	}

	r.Root, r.Index, r.MaxRequestBody = in.root, in.index, in.maxBody
	for _, st := range body(d) {
		x, ok := st.(*ast.Directive)
		if !ok {
			continue
		}
		switch x.Name {
		case "root":
			r.Root = c.path(str(arg(x, 0)))
		case "index":
			r.Index = strs(arg(x, 0))
		case "max_request_body":
			r.MaxRequestBody = size(arg(x, 0))
		case "rate_limit":
			r.RateLimit = rateLimit(x)
		case "cache":
			r.Cache = c.routeCache(x)
		}
	}
	r.Actions = actions(body(d))
	r.ErrorPages = c.errorPages(body(d), in.errorPages)

	childIn := inherited{root: r.Root, index: r.Index, maxBody: r.MaxRequestBody, errorPages: r.ErrorPages}
	for _, st := range body(d) {
		x, ok := st.(*ast.Directive)
		if !ok {
			continue
		}
		switch x.Name {
		case "rewrite":
			if rw, ok := c.rewrite(x); ok {
				r.Rewrites = append(r.Rewrites, rw)
			}
		case "route":
			if child, ok := c.route(x, childIn); ok {
				r.Children = append(r.Children, child)
			}
		case "match":
			if m, ok := c.match(x); ok {
				r.Matches = append(r.Matches, m)
			}
		}
	}
	return r, true
}

func (c *compiler) rewrite(d *ast.Directive) (model.Rewrite, bool) {
	pattern := str(arg(d, 0))
	re, err := regexp.Compile(pattern)
	if err != nil { // the validator checked this; keep the compiler honest
		c.diags.Error(d.Pos, diag.CodeInvalidRegex, "invalid regular expression")
		return model.Rewrite{}, false
	}
	flag := model.RewriteLast
	if f := str(arg(d, 2)); f != "" {
		flag = f
	}
	return model.Rewrite{Pattern: pattern, Regexp: re, Target: tmpl(arg(d, 1)), Flag: flag}, true
}

func actions(stmts []ast.Statement) model.Actions {
	var a model.Actions
	for _, st := range stmts {
		d, ok := st.(*ast.Directive)
		if !ok {
			continue
		}
		switch d.Name {
		case "try_files":
			a.TryFiles = tmpls(arg(d, 0))
		case "php":
			a.PHP = str(arg(d, 0))
		case "php_deny":
			a.PHPDeny = boolean(arg(d, 0))
		case "proxy":
			a.Proxy = str(arg(d, 0))
		case "proxy_set_header":
			a.ProxyHeaders = append(a.ProxyHeaders, model.Header{Name: str(arg(d, 0)), Value: str(arg(d, 1))})
		case "proxy_host":
			a.ProxyHostUpstream = str(arg(d, 0)) == "upstream"
		case "return":
			a.Return = &model.Return{Status: int(integer(arg(d, 0))), Target: tmpl(arg(d, 1))}
		}
	}
	return a
}

func (c *compiler) match(d *ast.Directive) (model.Match, bool) {
	var m model.Match
	ok := true
	for _, st := range body(d) {
		x, isDir := st.(*ast.Directive)
		if !isDir {
			continue
		}
		switch x.Name {
		case "method":
			m.Methods = strs(arg(x, 0))
		case "client_ip":
			op, rest := operatorAndRest(x.Args)
			if len(rest) == 1 {
				m.ClientIP = &model.ClientIPCond{Not: op == "!~", Networks: prefixes(rest[0])}
			}
		case "header", "query", "cookie":
			p, good := c.pattern(x)
			if !good {
				ok = false
				continue
			}
			switch x.Name {
			case "header":
				m.Headers = append(m.Headers, p)
			case "query":
				m.Query = append(m.Query, p)
			default:
				m.Cookies = append(m.Cookies, p)
			}
		case "scheme":
			m.Scheme = str(arg(x, 0))
		case "host":
			for _, h := range strs(arg(x, 0)) {
				m.Hosts = append(m.Hosts, strings.ToLower(h))
			}
		}
	}
	m.Actions = actions(body(d))
	return m, ok
}

func (c *compiler) pattern(d *ast.Directive) (model.Pattern, bool) {
	if len(d.Args) < 2 {
		return model.Pattern{}, false
	}
	p := model.Pattern{Name: str(d.Args[0]), Op: "="}
	valueArg := d.Args[1]
	if o, ok := d.Args[1].(*ast.Operator); ok && len(d.Args) == 3 {
		p.Op, valueArg = o.Op, d.Args[2]
	}
	p.Value = str(valueArg)
	switch p.Op {
	case "~", "~*", "!~", "!~*":
		pattern := p.Value
		if strings.HasSuffix(p.Op, "*") {
			pattern = "(?i)" + pattern
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			c.diags.Error(valueArg.Position(), diag.CodeInvalidRegex, "invalid regular expression")
			return p, false
		}
		p.Regexp = re
	}
	return p, true
}

// ---- cross checks ----

func (c *compiler) crossChecks(cfg *model.Config) {
	// Listener addresses must be unique.
	seenAddr := map[string]claim{}
	for _, cl := range c.listeners {
		if prev, dup := seenAddr[cl.key]; dup {
			c.diags.ErrorHint(cl.pos, diag.CodeDuplicateAddress,
				fmt.Sprintf("listeners %q and %q both bind %s", prev.owner, cl.owner, cl.key),
				fmt.Sprintf("first bound at %s; each listener needs its own address and port", prev.pos))
			continue
		}
		seenAddr[cl.key] = cl
	}

	// A hostname can belong to only one site (compared case-insensitively;
	// names were lowercased when sites were compiled).
	seenHost := map[string]claim{}
	for _, cl := range c.hosts {
		if prev, dup := seenHost[cl.key]; dup {
			if prev.idx != cl.idx {
				c.diags.ErrorHint(cl.pos, diag.CodeDuplicateHost,
					fmt.Sprintf("hostname %q is claimed by both site %q and site %q", cl.key, prev.owner, cl.owner),
					fmt.Sprintf("first claimed at %s; hostnames are compared case-insensitively", prev.pos))
			}
			continue
		}
		seenHost[cl.key] = cl
	}

	for i, s := range cfg.Sites {
		pos := c.sitePos[i]
		if len(s.Listeners) == 0 {
			c.diags.ErrorHint(pos, diag.CodeNoListener,
				fmt.Sprintf("site %q is attached to no listener", s.Name),
				"define a listen block, or list existing ones with `listeners [ ... ]`")
		}
		for _, ln := range s.Listeners {
			for _, l := range cfg.Listeners {
				if l.Name == ln && l.HTTPS && s.TLS == model.TLSOff {
					c.diags.ErrorHint(pos, diag.CodeTLSOffOnHTTPS,
						fmt.Sprintf("site %q has tls off but is attached to the https listener %q", s.Name, ln),
						"an https listener can only serve sites with TLS; use `tls manual` or `tls automatic`, or attach the site only to http listeners")
				}
			}
		}
		if s.TLS == model.TLSAutomatic {
			for _, n := range append([]string{s.Name}, s.Aliases...) {
				if strings.Contains(n, "*") && !dnsEnabled(cfg.TLS.ACME) {
					c.diags.ErrorHint(pos, diag.CodeInvalidValue,
						fmt.Sprintf("site %q uses automatic TLS but %q is a wildcard name", s.Name, n),
						"wildcard certificates need the DNS-01 challenge: add a dns block to acme, or list the host names, or use `tls manual` with a wildcard certificate")
				}
			}
		}
		if s.TLS == model.TLSAutomatic && cfg.TLS.ACME == nil {
			c.diags.ErrorHint(pos, diag.CodeSiteNoACME,
				fmt.Sprintf("site %q uses automatic TLS but there is no acme block", s.Name),
				"add `tls { acme { email \"you@example.com\" } }` to the server block, or set `tls manual` or `tls off` on the site")
		}
	}
}

// defaultACMEStorage is where certificates are kept when the configuration
// doesn't say.
func defaultACMEStorage() string {
	if runtime.GOOS == "windows" {
		if pd := os.Getenv("ProgramData"); pd != "" {
			return filepath.Join(pd, "lotus", "certs")
		}
		return `C:\ProgramData\lotus\certs`
	}
	return "/var/lib/lotus/certs"
}

// baseDir returns the absolute directory of the root configuration file.
func baseDir(rootFile string) string {
	dir := filepath.Dir(rootFile)
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

// path resolves a file path from the configuration: absolute paths are only
// cleaned, relative ones are taken relative to the root configuration file's
// directory. An empty path stays empty.
func (c *compiler) path(p string) string {
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(c.base, p)
}
