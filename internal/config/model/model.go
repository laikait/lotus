// Package model defines the typed, validated configuration the server runs on.
// The compiler builds it from the validated syntax tree: defaults are applied,
// inherited settings are merged, and patterns are compiled. A Config is
// treated as immutable once built.
package model

import (
	"encoding/json"
	"net/netip"
	"regexp"
	"time"
)

// Default values (docs: the "Default" field of each directive in the registry).
const (
	DefaultMaxConnections = 10000
	DefaultMaxRequestBody = 10 << 20
	DefaultMaxHeaderBytes = 64 << 10
	DefaultHeaderTimeout  = 15 * time.Second
	DefaultReadTimeout    = 60 * time.Second
	DefaultWriteTimeout   = 60 * time.Second
	DefaultIdleTimeout    = 120 * time.Second
	DefaultPHPTimeout     = 60 * time.Second
	DefaultPHPProcesses   = 4
	DefaultPHPMaxRequests = 500
	DefaultTLSMinVersion  = "1.2"
)

// Duration marshals to JSON as text such as "15s".
type Duration time.Duration

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

// Secret never prints its value: not with fmt, and not in JSON.
type Secret string

// String implements fmt.Stringer.
func (Secret) String() string { return "<redacted>" }

// GoString implements fmt.GoStringer.
func (Secret) GoString() string { return `"<redacted>"` }

// MarshalJSON implements json.Marshaler.
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"<redacted>"`), nil }

// Reveal returns the actual value. Use it only where the value is needed.
func (s Secret) Reveal() string { return string(s) }

// Config is the complete server configuration.
type Config struct {
	Version        int            `json:"config_version"`
	Runtime        Runtime        `json:"runtime"`
	Listeners      []Listener     `json:"listeners"`
	TrustedProxies []netip.Prefix `json:"trusted_proxies"`
	Limits         Limits         `json:"limits"`
	TLS            TLS            `json:"tls"`
	PHPBackends    []PHPBackend   `json:"php_backends"`
	Upstreams      []Upstream     `json:"upstreams,omitempty"`
	Sites          []Site         `json:"sites"`
	Control        Control        `json:"control"`
}

// Runtime holds Go runtime settings. Zero values mean "automatic" / "none".
type Runtime struct {
	MaxProcs    int   `json:"max_procs"` // 0 = auto
	MemoryLimit int64 `json:"memory_limit"`
}

// Listener is a network listener.
type Listener struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	HTTPS   bool   `json:"https"`
	HTTP3   bool   `json:"http3"`
}

// Limits are connection, size and timeout limits.
type Limits struct {
	MaxConnections int `json:"max_connections"`
	// MaxConnectionsPerIP limits connections from one client address; 0 is unlimited.
	MaxConnectionsPerIP int      `json:"max_connections_per_ip,omitempty"`
	MaxRequestBody      int64    `json:"max_request_body"`
	MaxHeaderBytes      int64    `json:"max_header_bytes"`
	HeaderTimeout       Duration `json:"header_timeout"`
	ReadTimeout         Duration `json:"read_timeout"`
	WriteTimeout        Duration `json:"write_timeout"`
	IdleTimeout         Duration `json:"idle_timeout"`
}

// DefaultLimits returns the built-in limits.
func DefaultLimits() Limits {
	return Limits{
		MaxConnections: DefaultMaxConnections,
		MaxRequestBody: DefaultMaxRequestBody,
		MaxHeaderBytes: DefaultMaxHeaderBytes,
		HeaderTimeout:  Duration(DefaultHeaderTimeout),
		ReadTimeout:    Duration(DefaultReadTimeout),
		WriteTimeout:   Duration(DefaultWriteTimeout),
		IdleTimeout:    Duration(DefaultIdleTimeout),
	}
}

// TLS holds server-wide TLS settings.
type TLS struct {
	MinVersion string `json:"min_version"`
	ACME       *ACME  `json:"acme,omitempty"`
}

// ACME configures automatic certificates.
type ACME struct {
	Email      string `json:"email,omitempty"`
	CA         string `json:"ca,omitempty"` // empty = Let's Encrypt production
	EABKeyID   string `json:"eab_key_id,omitempty"`
	EABHMACKey Secret `json:"eab_hmac_key,omitempty"`
}

// PHPBackend is a FastCGI backend: either an existing server at Address, or
// processes LOTUS starts itself from Command.
type PHPBackend struct {
	Name    string   `json:"name"`
	Network string   `json:"network,omitempty"` // "unix" or "tcp"
	Address string   `json:"address,omitempty"` // socket path or host:port
	Timeout Duration `json:"timeout"`

	// Set when LOTUS runs the processes (Command is not empty).
	Command     string `json:"command,omitempty"`
	Processes   int    `json:"processes,omitempty"`
	MaxRequests int    `json:"max_requests,omitempty"` // 0: never recycle
	PHPIni      string `json:"php_ini,omitempty"`
}

// Upstream is a group of reverse-proxy backends.
type Upstream struct {
	Name            string            `json:"name"`
	Backends        []UpstreamBackend `json:"backends"`
	Balance         string            `json:"balance"`
	ConnectTimeout  Duration          `json:"connect_timeout"`
	ResponseTimeout Duration          `json:"response_timeout"`
	IdleTimeout     Duration          `json:"idle_timeout"`
	Retries         int               `json:"retries"`
	MaxFails        int               `json:"max_fails"`
	FailTimeout     Duration          `json:"fail_timeout"`
	MaxConns        int               `json:"max_conns,omitempty"`
	Health          *HealthCheck      `json:"health_check,omitempty"`
	TLSVerify       bool              `json:"tls_verify"`
	TLSCA           string            `json:"tls_ca,omitempty"`
	TLSServerName   string            `json:"tls_server_name,omitempty"`
	TLSClientCert   string            `json:"tls_client_cert,omitempty"`
	TLSClientKey    string            `json:"tls_client_key,omitempty"`
}

// UpstreamBackend is one server of an upstream.
type UpstreamBackend struct {
	Address  string `json:"address"` // as written: http://host:port, https://host:port or unix:/path
	Weight   int    `json:"weight"`
	MaxConns int    `json:"max_conns,omitempty"`
}

// HealthCheck is the active health check of an upstream.
type HealthCheck struct {
	Path               string   `json:"path,omitempty"`
	Interval           Duration `json:"interval"`
	Timeout            Duration `json:"timeout"`
	HealthyThreshold   int      `json:"healthy_threshold"`
	UnhealthyThreshold int      `json:"unhealthy_threshold"`
}

// Defaults of an upstream.
const (
	DefaultUpstreamConnectTimeout  = 5 * time.Second
	DefaultUpstreamResponseTimeout = 60 * time.Second
	DefaultUpstreamIdleTimeout     = 90 * time.Second
	DefaultUpstreamRetries         = 1
	DefaultUpstreamMaxFails        = 3
	DefaultUpstreamFailTimeout     = 10 * time.Second
)

// Managed reports whether LOTUS starts the backend's processes.
func (b *PHPBackend) Managed() bool { return b.Command != "" }

// Control configures the local control channel. Empty values mean the
// platform default.
type Control struct {
	Socket      string `json:"socket,omitempty"`
	ViewerGroup string `json:"viewer_group,omitempty"`
}

// TLS modes of a site.
// Symlink and dotfile policies of a site.
const (
	SymlinksFollow     = "follow"
	SymlinksOwnerMatch = "owner_match"
	SymlinksDeny       = "deny"
	DotfilesDeny       = "deny"
	DotfilesAllow      = "allow"
)

const (
	TLSAutomatic = "automatic"
	TLSManual    = "manual"
	TLSOff       = "off"
)

// Site is a virtual host.
type Site struct {
	Name            string   `json:"name"`
	Aliases         []string `json:"aliases"`
	Listeners       []string `json:"listeners"`
	TLS             string   `json:"tls"`
	Certificate     string   `json:"certificate,omitempty"`     // PEM chain, for tls manual
	CertificateKey  string   `json:"certificate_key,omitempty"` // PEM key, for tls manual
	Symlinks        string   `json:"symlinks"`                  // follow, owner_match or deny
	Dotfiles        string   `json:"dotfiles"`                  // deny or allow
	Root            string   `json:"root,omitempty"`
	Index           []string `json:"index"`
	Limits          Limits   `json:"limits"`
	Logs            Logs     `json:"logs"`
	SecurityHeaders []Header `json:"security_headers"`
	Routes          []Route  `json:"routes"`
	Named           []Route  `json:"named_routes"`
	// ErrorPages maps a status to the page shown for it: "/path" or "@name".
	ErrorPages map[int]string `json:"error_pages,omitempty"`
	// HTAccess is set when the site reads .htaccess files.
	HTAccess *HTAccess `json:"htaccess,omitempty"`
	// RateLimit limits the requests of every client of the site.
	RateLimit *RateLimit `json:"rate_limit,omitempty"`
}

// RateLimit is a token-bucket limit on requests.
type RateLimit struct {
	Requests int      `json:"requests"`
	Per      Duration `json:"per"`
	Burst    int      `json:"burst"`
	Key      string   `json:"key"` // RateKeyIP or RateKeyGlobal
}

// What a rate limit counts separately.
const (
	RateKeyIP     = "ip"
	RateKeyGlobal = "global"
)

// What a site does with .htaccess directives it can't honour.
const (
	HTAccessError  = "error"  // answer 500 and log the directive
	HTAccessIgnore = "ignore" // skip it with a warning (never for access rules)
)

// DefaultHTAccessRecheck is how long a .htaccess file is remembered.
const DefaultHTAccessRecheck = 2 * time.Second

// Defaults of the Basic authentication lock-out.
const (
	DefaultHTAccessAuthFailures = 10
	DefaultHTAccessAuthLockout  = 5 * time.Minute
)

// HTAccess configures .htaccess support for a site.
type HTAccess struct {
	Allow         []string `json:"allow"`          // permitted directive categories
	OnUnsupported string   `json:"on_unsupported"` // HTAccessError or HTAccessIgnore
	Recheck       Duration `json:"recheck"`
	// AuthFailures wrong Basic attempts within AuthLockout lock a client address
	// out for AuthLockout; 0 turns the lock-out off.
	AuthFailures int      `json:"auth_failures"`
	AuthLockout  Duration `json:"auth_lockout"`
}

// Header is a response header added to every response.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// LogTarget is where one log goes.
type LogTarget struct {
	Path string `json:"path,omitempty"` // empty with Off=false: default destination
	Off  bool   `json:"off,omitempty"`
}

// Logs are a site's log destinations.
type Logs struct {
	Access LogTarget `json:"access"`
	Error  LogTarget `json:"error"`
}

// RouteKind is how a route matches a path.
type RouteKind string

// Route kinds (docs/architecture/routing-precedence.md §2).
const (
	RoutePrefix      RouteKind = "prefix"
	RouteFinalPrefix RouteKind = "final_prefix"
	RouteExact       RouteKind = "exact"
	RouteRegex       RouteKind = "regex"
	RouteRegexFold   RouteKind = "regex_fold"
	RouteNamed       RouteKind = "named"
)

// Route handles requests whose path matches. Root, Index and MaxRequestBody
// are the effective values after inheritance from the site and parent routes.
type Route struct {
	Kind           RouteKind      `json:"kind"`
	Path           string         `json:"path"` // path, pattern, or @name
	Regexp         *regexp.Regexp `json:"-"`
	Root           string         `json:"root,omitempty"`
	Index          []string       `json:"index"`
	MaxRequestBody int64          `json:"max_request_body"`
	Actions        Actions        `json:"actions"`
	Rewrites       []Rewrite      `json:"rewrites,omitempty"`
	// ErrorPages are the effective error pages: the site's and enclosing
	// routes', replaced one by one by this route's own.
	ErrorPages map[int]string `json:"error_pages,omitempty"`
	Matches    []Match        `json:"matches,omitempty"`
	Children   []Route        `json:"children,omitempty"`
	// RateLimit is this route's own limit; the site's applies as well.
	RateLimit *RateLimit `json:"rate_limit,omitempty"`
}

// LiteralBrace is written before a '{' in a request-time template (try_files
// candidates, return targets) that must not start a placeholder: one written
// as \{ or coming from env() or file(). The router removes the marker.
const LiteralBrace = "\x00"

// Rewrite flags.
const (
	RewriteLast      = "last"      // route again with the new URI
	RewriteRedirect  = "redirect"  // answer 302
	RewritePermanent = "permanent" // answer 301
)

// Rewrite replaces the request when its path matches Pattern. Target is a
// request-time template; {re.N} are the pattern's capture groups.
type Rewrite struct {
	Pattern string         `json:"pattern"`
	Regexp  *regexp.Regexp `json:"-"`
	Target  string         `json:"target"`
	Flag    string         `json:"flag"`
}

// Actions say what to do with a request. A nil/zero field means "not set".
type Actions struct {
	TryFiles []string `json:"try_files,omitempty"`
	PHP      string   `json:"php,omitempty"` // name of a PHP backend
	PHPDeny  bool     `json:"php_deny,omitempty"`
	Proxy    string   `json:"proxy,omitempty"` // name of an upstream
	// ProxyHeaders are set on proxied requests (an empty value removes the
	// header); ProxyHostUpstream sends the backend's own Host instead of the client's.
	ProxyHeaders      []Header `json:"proxy_headers,omitempty"`
	ProxyHostUpstream bool     `json:"proxy_host_upstream,omitempty"`
	Return            *Return  `json:"return,omitempty"`
}

// Return is an immediate response.
type Return struct {
	Status int    `json:"status"`
	Target string `json:"target,omitempty"`
}

// Match applies extra actions when all conditions hold.
type Match struct {
	Methods  []string      `json:"methods,omitempty"`
	ClientIP *ClientIPCond `json:"client_ip,omitempty"`
	Headers  []Pattern     `json:"headers,omitempty"`
	Query    []Pattern     `json:"query,omitempty"`
	Cookies  []Pattern     `json:"cookies,omitempty"`
	Scheme   string        `json:"scheme,omitempty"`
	Hosts    []string      `json:"hosts,omitempty"`
	Actions  Actions       `json:"actions"`
}

// ClientIPCond tests the resolved client IP against networks.
type ClientIPCond struct {
	Not      bool           `json:"not,omitempty"` // true: client is in none of Networks
	Networks []netip.Prefix `json:"networks"`
}

// Pattern tests a header, query parameter or cookie.
type Pattern struct {
	Name   string         `json:"name"`
	Op     string         `json:"op"` // = ~ ~* !~ !~*
	Value  string         `json:"value"`
	Regexp *regexp.Regexp `json:"-"`
}

// UsesPHP reports whether any route of the site, or match block in one, runs PHP.
func (s *Site) UsesPHP() bool { return routesUsePHP(s.Routes) || routesUsePHP(s.Named) }

func routesUsePHP(routes []Route) bool {
	for i := range routes {
		r := &routes[i]
		if r.Actions.PHP != "" || routesUsePHP(r.Children) {
			return true
		}
		for _, m := range r.Matches {
			if m.Actions.PHP != "" {
				return true
			}
		}
	}
	return false
}
