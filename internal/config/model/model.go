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
	MaxConnections int      `json:"max_connections"`
	MaxRequestBody int64    `json:"max_request_body"`
	MaxHeaderBytes int64    `json:"max_header_bytes"`
	HeaderTimeout  Duration `json:"header_timeout"`
	ReadTimeout    Duration `json:"read_timeout"`
	WriteTimeout   Duration `json:"write_timeout"`
	IdleTimeout    Duration `json:"idle_timeout"`
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

// PHPBackend is a FastCGI backend.
type PHPBackend struct {
	Name    string   `json:"name"`
	Network string   `json:"network"` // "unix" or "tcp"
	Address string   `json:"address"` // socket path or host:port
	Timeout Duration `json:"timeout"`
}

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
	Return   *Return  `json:"return,omitempty"`
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
