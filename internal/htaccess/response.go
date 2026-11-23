package htaccess

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Response policy: what a file says about the response itself rather than about
// whether, or where, to serve it.
//
//	Expires*           -> Cache-Control: max-age and Expires
//	AddDefaultCharset  -> a charset on text/html and text/plain
//	AddType            -> the Content-Type of static files by extension
//	php_value/php_flag -> PHP settings for scripts of the directory
//
// All of it is data selecting among behaviours LOTUS already has.

// phpDenied are PHP settings a site file may not change. They either loosen the
// sandbox the administrator built (open_basedir, disable_*), load code or files
// from outside the site (extension, auto_*_file, the stream wrapper switches), or
// choose where the server writes (error_log, session.save_path, temp dirs).
var phpDenied = map[string]string{
	"open_basedir": "it is part of the isolation of the site", "disable_functions": "it is part of the isolation of the site",
	"disable_classes": "it is part of the isolation of the site", "doc_root": "it is set by the server", "user_dir": "it is set by the server",
	"extension": "extensions are loaded by the administrator", "zend_extension": "extensions are loaded by the administrator",
	"extension_dir": "extensions are loaded by the administrator", "allow_url_include": "it would let a file include remote code",
	"allow_url_fopen": "it is decided by the administrator", "auto_prepend_file": "it would run a file before every script",
	"auto_append_file": "it would run a file after every script", "error_log": "it chooses a file the server writes to",
	"sys_temp_dir": "it chooses where the server writes", "upload_tmp_dir": "it chooses where the server writes",
	"session.save_path": "it chooses where the server writes", "include_path": "it can point outside the site",
	"enable_dl": "it is decided by the administrator", "cgi.fix_pathinfo": "it is decided by the administrator",
	"sendmail_path": "it chooses a program to run", "mail.log": "it chooses a file the server writes to",
	"browscap": "it chooses a file the server reads", "curl.cainfo": "it chooses a file the server reads",
	"openssl.cafile": "it chooses a file the server reads", "openssl.capath": "it chooses a file the server reads",
	"opcache.preload": "it would run a file at start-up", "opcache.error_log": "it chooses a file the server writes to",
	"syslog.filter": "it is decided by the administrator", "xdebug.output_dir": "it chooses where the server writes",
}

// PHPSetting is one php_value or php_flag.
type PHPSetting struct {
	Name, Value string
}

// ExpiresPolicy is mod_expires for a request.
type ExpiresPolicy struct {
	Default    time.Duration
	HasDefault bool
	ByType     map[string]time.Duration // "text/css", or "image/*"
}

// ResponsePolicy is applied when the response starts.
type ResponsePolicy struct {
	Expires *ExpiresPolicy
	// Charset is added to text/html and text/plain responses without one.
	Charset string
	// Types maps a lower-case extension with its dot (".woff2") to a Content-Type.
	Types map[string]string
}

// Empty says there is nothing to apply.
func (p *ResponsePolicy) Empty() bool {
	return p == nil || (p.Expires == nil && p.Charset == "" && len(p.Types) == 0)
}

// cacheableStatus lists the statuses mod_expires adds lifetimes to.
func cacheableStatus(code int) bool {
	return code == http.StatusOK || code == http.StatusPartialContent || code == http.StatusNotModified
}

// Apply changes the headers of a response that is about to start. script says
// the response comes from a PHP script, whose Content-Type is its own business.
func (p *ResponsePolicy) Apply(h http.Header, path string, status int, script bool, now time.Time) {
	if p.Empty() {
		return
	}
	if !script && len(p.Types) > 0 && cacheableStatus(status) {
		if i := strings.LastIndexByte(path, '.'); i >= 0 && !strings.Contains(path[i:], "/") {
			if t, ok := p.Types[strings.ToLower(path[i:])]; ok {
				h.Set("Content-Type", t)
			}
		}
	}
	ct := h.Get("Content-Type")
	media := strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	if p.Charset != "" && (media == "text/html" || media == "text/plain") && !strings.Contains(strings.ToLower(ct), "charset=") {
		h.Set("Content-Type", ct+"; charset="+p.Charset)
	}
	if e := p.Expires; e != nil && cacheableStatus(status) {
		d, ok := e.lifetime(media)
		if !ok || h.Get("Expires") != "" {
			return // nothing to say, or the script already decided
		}
		secs := int64(d / time.Second)
		if cc := h.Get("Cache-Control"); cc != "" {
			if strings.Contains(strings.ToLower(cc), "max-age") {
				return
			}
			h.Set("Cache-Control", cc+", max-age="+strconv.FormatInt(secs, 10))
		} else {
			h.Set("Cache-Control", "max-age="+strconv.FormatInt(secs, 10))
		}
		h.Set("Expires", now.Add(d).UTC().Format(http.TimeFormat))
	}
}

func (e *ExpiresPolicy) lifetime(media string) (time.Duration, bool) {
	if d, ok := e.ByType[media]; ok {
		return d, true
	}
	if i := strings.IndexByte(media, '/'); i >= 0 {
		if d, ok := e.ByType[media[:i]+"/*"]; ok {
			return d, true
		}
	}
	if e.HasDefault {
		return e.Default, true
	}
	return 0, false
}

// ---- compile ----

// settings are what a file says about the response, collected while compiling.
type settings struct {
	expiresSet    bool
	expiresActive bool
	expDefault    time.Duration
	hasExpDefault bool
	expByType     map[string]time.Duration
	charsetSet    bool
	charset       string
	types         map[string]string
	php           []PHPSetting
}

var (
	charsetRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:+-]{0,63}$`)
	phpNameRe = regexp.MustCompile(`^[A-Za-z0-9_.]{1,128}$`)
	mimeRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]*/[a-z0-9*][a-z0-9!#$&^_.+-]*$`)
)

func (c *compiler) settings() *settings {
	if c.out.settings == nil {
		c.out.settings = &settings{}
	}
	return c.out.settings
}

func (c *compiler) expires(n *Node) {
	st := c.settings()
	switch n.Name {
	case "expiresactive":
		if len(n.Args) != 1 {
			c.bad(n.Line, n.Name, "use `ExpiresActive On` or `ExpiresActive Off`", false)
			return
		}
		switch strings.ToLower(n.Args[0]) {
		case "on":
			st.expiresSet, st.expiresActive = true, true
		case "off":
			st.expiresSet, st.expiresActive = true, false
		default:
			c.bad(n.Line, n.Name, "use `ExpiresActive On` or `ExpiresActive Off`", false)
			return
		}
	case "expiresdefault":
		if len(n.Args) != 1 {
			c.bad(n.Line, n.Name, `use: ExpiresDefault "access plus 1 month"`, false)
			return
		}
		d, err := parseLifetime(n.Args[0])
		if err != nil {
			c.bad(n.Line, n.Name, err.Error(), false)
			return
		}
		st.expDefault, st.hasExpDefault = d, true
	case "expiresbytype":
		if len(n.Args) != 2 {
			c.bad(n.Line, n.Name, `use: ExpiresByType image/png "access plus 1 year"`, false)
			return
		}
		typ := strings.ToLower(n.Args[0])
		if !mimeRe.MatchString(typ) || (strings.Contains(typ, "*") && !strings.HasSuffix(typ, "/*")) {
			c.bad(n.Line, n.Name, fmt.Sprintf("%q is not a media type (exact types and `image/*` are supported)", n.Args[0]), false)
			return
		}
		d, err := parseLifetime(n.Args[1])
		if err != nil {
			c.bad(n.Line, n.Name, err.Error(), false)
			return
		}
		if st.expByType == nil {
			st.expByType = map[string]time.Duration{}
		}
		st.expByType[typ] = d
	}
	c.ok(n)
}

var lifetimeUnits = map[string]time.Duration{
	"second": time.Second, "minute": time.Minute, "hour": time.Hour, "day": 24 * time.Hour,
	"week": 7 * 24 * time.Hour, "month": 30 * 24 * time.Hour, "year": 365 * 24 * time.Hour,
}

// parseLifetime reads "access plus 1 month 2 days", "now plus 3 hours" and the
// short form "A3600" (seconds after access). Lifetimes counted from the file's
// modification time ("modification plus ...", "M3600") need the file's date,
// which the response policy doesn't have, so they are refused.
func parseLifetime(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if len(s) > 1 && (s[0] == 'A' || s[0] == 'a') && s[1] >= '0' && s[1] <= '9' {
		n, err := strconv.ParseInt(s[1:], 10, 64)
		if err != nil || n < 0 || n > 10*365*86400 {
			return 0, fmt.Errorf("%q is not a lifetime", s)
		}
		return time.Duration(n) * time.Second, nil
	}
	f := strings.Fields(strings.ToLower(s))
	if len(f) >= 1 && (f[0] == "modification" || f[0] == "m" || (len(s) > 1 && (s[0] == 'M' || s[0] == 'm') && s[1] >= '0' && s[1] <= '9')) {
		return 0, fmt.Errorf("%q counts from the file's modification time, which is not supported; use `access plus ...`", s)
	}
	if len(f) < 4 || (f[0] != "access" && f[0] != "now") || f[1] != "plus" || (len(f)-2)%2 != 0 {
		return 0, fmt.Errorf(`%q is not a lifetime; use "access plus 1 month" or "A2592000"`, s)
	}
	var total time.Duration
	for i := 2; i < len(f); i += 2 {
		n, err := strconv.ParseInt(f[i], 10, 32)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("%q is not a number in %q", f[i], s)
		}
		u, ok := lifetimeUnits[strings.TrimSuffix(f[i+1], "s")]
		if !ok {
			return 0, fmt.Errorf("%q is not a unit of time in %q", f[i+1], s)
		}
		if n > int64(10*365*24*time.Hour/u) {
			return 0, fmt.Errorf("%q is longer than ten years", s)
		}
		total += time.Duration(n) * u
		if total > 10*365*24*time.Hour {
			return 0, fmt.Errorf("%q is longer than ten years", s)
		}
	}
	return total, nil
}

func (c *compiler) charset(n *Node) {
	if len(n.Args) != 1 {
		c.bad(n.Line, n.Name, "use `AddDefaultCharset UTF-8` or `AddDefaultCharset Off`", false)
		return
	}
	st := c.settings()
	switch v := n.Args[0]; {
	case strings.EqualFold(v, "off"):
		st.charsetSet, st.charset = true, ""
	case strings.EqualFold(v, "on"):
		c.bad(n.Line, n.Name, "name the character set (AddDefaultCharset UTF-8) instead of On", false)
		return
	case charsetRe.MatchString(v):
		st.charsetSet, st.charset = true, v
	default:
		c.bad(n.Line, n.Name, fmt.Sprintf("%q is not a character set name", v), false)
		return
	}
	c.ok(n)
}

func (c *compiler) addType(n *Node) {
	if len(n.Args) < 2 {
		c.bad(n.Line, n.Name, "use: AddType media/type .ext [.ext...]", false)
		return
	}
	typ := strings.ToLower(n.Args[0])
	if strings.HasPrefix(typ, "application/x-httpd-") || strings.HasPrefix(typ, "application/x-php") || typ == "text/x-server-parsed-html" {
		// Handlers (application/x-httpd-php) say which files run as code. That is
		// decided by the server's `php` route, never by a file a tenant controls.
		c.bad(n.Line, n.Name, n.Name+" "+n.Args[0]+" would choose a handler: which files run as PHP is decided by the server's `php` route", false)
		return
	}
	if !mimeRe.MatchString(typ) || strings.Contains(typ, "*") {
		c.bad(n.Line, n.Name, fmt.Sprintf("%q is not a media type", n.Args[0]), false)
		return
	}
	st := c.settings()
	if st.types == nil {
		st.types = map[string]string{}
	}
	for _, ext := range n.Args[1:] {
		e := strings.ToLower(ext)
		if !strings.HasPrefix(e, ".") {
			e = "." + e
		}
		if len(e) < 2 || strings.ContainsAny(e[1:], "./\\ ") {
			c.bad(n.Line, n.Name, fmt.Sprintf("%q is not a file extension", ext), false)
			return
		}
		st.types[e] = typ
	}
	c.ok(n)
}

func (c *compiler) php(n *Node) {
	if len(n.Args) < 2 {
		c.bad(n.Line, n.Name, "use: "+n.Name+" name value", false)
		return
	}
	name := n.Args[0]
	if !phpNameRe.MatchString(name) {
		c.bad(n.Line, n.Name, fmt.Sprintf("%q is not a PHP setting name", name), false)
		return
	}
	if why, bad := phpDenied[strings.ToLower(name)]; bad {
		c.bad(n.Line, n.Name, fmt.Sprintf("a site file can't change %s: %s", name, why), false)
		return
	}
	var val string
	if n.Name == "php_flag" {
		if len(n.Args) != 2 {
			c.bad(n.Line, n.Name, "use: php_flag name on|off", false)
			return
		}
		switch strings.ToLower(n.Args[1]) {
		case "on", "1", "true", "yes":
			val = "On"
		case "off", "0", "false", "no", "none":
			val = "Off"
		default:
			c.bad(n.Line, n.Name, "php_flag takes on or off", false)
			return
		}
	} else {
		if len(n.Args) != 2 {
			c.bad(n.Line, n.Name, "use: php_value name value (quote a value that contains spaces)", false)
			return
		}
		val = n.Args[1]
		if strings.Contains(val, "'") {
			c.bad(n.Line, n.Name, "a PHP setting value can't contain a single quote", false)
			return
		}
		for i := 0; i < len(val); i++ {
			if val[i] < 0x20 || val[i] == 0x7f {
				c.bad(n.Line, n.Name, "a PHP setting value can't contain control characters", false)
				return
			}
		}
	}
	c.settings().php = append(c.settings().php, PHPSetting{name, val})
	c.ok(n)
}

// ---- evaluate ----

// merge combines the settings of the layers, root first: a deeper file
// overrides what a shallower one set (ExpiresByType per type, the rest as a whole).
func mergeSettings(layers []Layer) (*ResponsePolicy, []PHPSetting) {
	var (
		pol     = &ResponsePolicy{}
		php     []PHPSetting
		active  bool
		exp     = &ExpiresPolicy{ByType: map[string]time.Duration{}}
		anyExp  bool
		phpSeen = map[string]int{}
	)
	for _, l := range layers {
		st := l.File.settings
		if st == nil {
			continue
		}
		if st.expiresSet {
			active, anyExp = st.expiresActive, true
		}
		if st.hasExpDefault {
			exp.Default, exp.HasDefault = st.expDefault, true
		}
		for k, v := range st.expByType {
			exp.ByType[k] = v
		}
		if st.charsetSet {
			pol.Charset = st.charset
		}
		for k, v := range st.types {
			if pol.Types == nil {
				pol.Types = map[string]string{}
			}
			pol.Types[k] = v
		}
		for _, s := range st.php {
			if i, ok := phpSeen[s.Name]; ok {
				php[i].Value = s.Value
				continue
			}
			phpSeen[s.Name] = len(php)
			php = append(php, s)
		}
	}
	if anyExp && active {
		pol.Expires = exp
	}
	if pol.Empty() {
		pol = nil
	}
	return pol, php
}
