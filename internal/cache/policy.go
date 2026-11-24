// Package cache is the response cache of LOTUS (docs/architecture/cache.md).
//
// It stores responses of proxied and PHP routes in memory, under a byte limit,
// and answers repeated requests from there. It follows the rules of a shared
// HTTP cache (RFC 9111): what a backend marks private or no-store is never
// kept, a response that sets a cookie is never kept, a request that carries
// credentials never touches the cache, and the headers a response varies on
// are part of what makes two requests the same.
package cache

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// maxSeconds caps a lifetime so that arithmetic on it can't overflow.
const maxSeconds = 10 * 365 * 24 * 60 * 60

// maxVaryNames is the most headers a response may vary on and still be kept.
// A response that varies on more is not worth the combinations it creates.
const maxVaryNames = 6

// cacheControl is what a response's Cache-Control header says that matters to a
// shared cache.
type cacheControl struct {
	noStore, noCache, private bool
	revalidate                bool // must-revalidate or proxy-revalidate: never serve it stale
	maxAge, sMaxAge           time.Duration
	hasMaxAge, hasSMaxAge     bool
}

func parseControl(h http.Header) cacheControl {
	var c cacheControl
	for _, line := range h.Values("Cache-Control") {
		for _, part := range splitDirectives(line) {
			name, val, _ := strings.Cut(part, "=")
			name = strings.ToLower(strings.TrimSpace(name))
			val = strings.Trim(strings.TrimSpace(val), `"`)
			switch name {
			case "no-store":
				c.noStore = true
			case "no-cache":
				c.noCache = true
			case "private":
				c.private = true
			case "must-revalidate", "proxy-revalidate":
				c.revalidate = true
			case "max-age":
				d := seconds(val)
				if !c.hasMaxAge || d < c.maxAge { // conflicting values: the smaller one
					c.maxAge = d
				}
				c.hasMaxAge = true
			case "s-maxage":
				d := seconds(val)
				if !c.hasSMaxAge || d < c.sMaxAge {
					c.sMaxAge = d
				}
				c.hasSMaxAge = true
			}
		}
	}
	return c
}

// splitDirectives splits a header value at the commas that are not inside a
// quoted string.
func splitDirectives(s string) []string {
	var out []string
	start, quoted := 0, false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			quoted = !quoted
		case '\\':
			if quoted {
				i++
			}
		case ',':
			if !quoted {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// seconds reads a delta-seconds value. A value that isn't one means "already
// stale", as RFC 9111 says.
func seconds(v string) time.Duration {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	if n > maxSeconds {
		n = maxSeconds
	}
	return time.Duration(n) * time.Second
}

// storableStatus lists the statuses a response may be kept with.
func storableStatus(code int) bool {
	switch code {
	case 200, 203, 204, 301, 308, 404, 410:
		return true
	}
	return false
}

func negativeStatus(code int) bool { return code == 404 || code == 410 }

// freshness says whether a response may be stored and for how long it is fresh.
// ok is false for anything a shared cache must not keep. noStale is set when the
// response forbids being served stale.
func freshness(status int, h http.Header, now time.Time, o Options) (ttl time.Duration, noStale, ok bool) {
	if !storableStatus(status) {
		return 0, false, false
	}
	cc := parseControl(h)
	if cc.noStore || cc.private || cc.noCache {
		return 0, false, false
	}
	// A response that sets a cookie is for one client.
	if len(h.Values("Set-Cookie")) > 0 || len(h.Values("Set-Cookie2")) > 0 {
		return 0, false, false
	}
	// Streams, trailers and partial content are not whole, reusable answers.
	if h.Get("Trailer") != "" || h.Get("Content-Range") != "" {
		return 0, false, false
	}
	if ct := strings.ToLower(h.Get("Content-Type")); strings.HasPrefix(ct, "text/event-stream") {
		return 0, false, false
	}

	explicit := true
	switch {
	case cc.hasSMaxAge:
		ttl = cc.sMaxAge
	case cc.hasMaxAge:
		ttl = cc.maxAge
	case h.Get("Expires") != "":
		exp, err := http.ParseTime(h.Get("Expires"))
		if err != nil {
			ttl = 0 // an invalid Expires (such as "0") means already expired
			break
		}
		base := now
		if d, err := http.ParseTime(h.Get("Date")); err == nil {
			base = d
		}
		ttl = exp.Sub(base)
	default:
		explicit = false
		if len(h.Values("Cache-Control")) == 0 && strings.Contains(strings.ToLower(h.Get("Pragma")), "no-cache") {
			return 0, false, false
		}
		ttl = o.TTL
		if negativeStatus(status) {
			ttl = o.NegativeTTL
		}
	}
	if explicit {
		if age := seconds(strings.TrimSpace(h.Get("Age"))); age > 0 {
			ttl -= age
		}
	}
	return ttl, cc.revalidate, ttl > 0
}

// varyOf returns the header names (lower case, sorted) a response varies on. ok
// is false when it varies on everything ("*") or on too many headers.
func varyOf(h http.Header) (names []string, ok bool) {
	seen := map[string]bool{}
	for _, line := range h.Values("Vary") {
		for _, n := range strings.Split(line, ",") {
			n = strings.ToLower(strings.TrimSpace(n))
			switch {
			case n == "":
			case n == "*":
				return nil, false
			case !seen[n]:
				seen[n] = true
				names = append(names, n)
			}
		}
	}
	if len(names) > maxVaryNames {
		return nil, false
	}
	sort.Strings(names)
	return names, true
}

// varyValue is what the request has in the headers a response varies on. Two
// requests with the same value get the same stored response.
func varyValue(names []string, req http.Header) string {
	if len(names) == 0 {
		return ""
	}
	var b strings.Builder
	for _, n := range names {
		vals := req.Values(n)
		if n == "accept-encoding" {
			b.WriteString(normalizeEncoding(vals))
		} else {
			b.WriteString(strings.TrimSpace(strings.Join(vals, ",")))
		}
		b.WriteByte(0)
	}
	return b.String()
}

// normalizeEncoding makes "gzip, br", "br,gzip" and "br;q=1, gzip;q=0.5" the
// same value: the set of codings the client accepts, without weights. Codings
// with q=0 are refused ones and don't count.
func normalizeEncoding(values []string) string {
	set := map[string]bool{}
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			name, params, _ := strings.Cut(part, ";")
			name = strings.ToLower(strings.TrimSpace(name))
			if name == "" {
				continue
			}
			if q, ok := qValue(params); ok && q == 0 {
				continue
			}
			set[name] = true
		}
	}
	list := make([]string, 0, len(set))
	for n := range set {
		list = append(list, n)
	}
	sort.Strings(list)
	return strings.Join(list, ",")
}

func qValue(params string) (float64, bool) {
	for _, p := range strings.Split(params, ";") {
		k, v, ok := strings.Cut(p, "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), "q") {
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			return f, err == nil
		}
	}
	return 0, false
}

// CookieMode says how the Cookie header of a request affects caching.
type CookieMode int

const (
	// CookiesAny: a request with any cookie never uses the cache. This is the
	// safe default: a cookie usually means a session.
	CookiesAny CookieMode = iota
	// CookiesIgnore: cookies don't matter.
	CookiesIgnore
	// CookiesNamed: only the cookies in Options.CookieNames matter (a trailing *
	// matches a prefix); other cookies are ignored.
	CookiesNamed
)

// Options are the settings of one route's cache.
type Options struct {
	// TTL is how long a response is fresh when the backend says nothing about
	// it. 0 keeps only responses that carry their own lifetime.
	TTL time.Duration
	// NegativeTTL is the same for 404 and 410 responses.
	NegativeTTL time.Duration
	// StaleIfError is how long past its lifetime an entry may still be served
	// when the backend fails (a 500, 502, 503 or 504, or no answer at all).
	StaleIfError time.Duration
	// LockTimeout is how long a request waits for another one that is fetching
	// the same thing. 0 turns the wait off: every miss goes to the backend.
	LockTimeout time.Duration
	Cookies     CookieMode
	CookieNames []string
}

// skip reports whether a request must neither be answered from the cache nor
// store anything.
func skip(r *http.Request, o Options) bool {
	if r.Header.Get("Authorization") != "" || r.Header.Get("Upgrade") != "" {
		return true
	}
	if r.ContentLength > 0 || len(r.TransferEncoding) > 0 {
		return true // a GET with a body: the body may change the answer
	}
	switch o.Cookies {
	case CookiesIgnore:
		return false
	case CookiesNamed:
		for _, c := range r.Cookies() {
			for _, pat := range o.CookieNames {
				if cookieMatches(pat, c.Name) {
					return true
				}
			}
		}
		return false
	}
	return len(r.Header.Values("Cookie")) > 0
}

func cookieMatches(pattern, name string) bool {
	if p, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(name, p)
	}
	return pattern == name
}
