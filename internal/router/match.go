package router

import (
	"net/http"
	"net/netip"
	"strings"

	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/vhost"
)

// Request is what match conditions look at.
type Request struct {
	HTTP     *http.Request // method, headers, query and cookies are read from here
	ClientIP netip.Addr    // the resolved client address; invalid if unknown
	Scheme   string        // "http" or "https"
}

// SelectMatch returns the first match block of route whose conditions all
// hold, or nil. Later blocks are not looked at (routing-precedence.md §4).
func SelectMatch(route *model.Route, req Request) *model.Match {
	for i := range route.Matches {
		if holds(&route.Matches[i], req) {
			return &route.Matches[i]
		}
	}
	return nil
}

// Effective returns the actions to run: the route's own, with those of the
// applicable match block replacing the actions of the same name.
func Effective(route *model.Route, m *model.Match) model.Actions {
	a := route.Actions
	if m == nil {
		return a
	}
	if m.Actions.TryFiles != nil {
		a.TryFiles = m.Actions.TryFiles
	}
	if m.Actions.PHP != "" {
		a.PHP = m.Actions.PHP
	}
	if m.Actions.PHPDeny {
		a.PHPDeny = true
	}
	if m.Actions.Proxy != "" {
		a.Proxy = m.Actions.Proxy
	}
	if m.Actions.Return != nil {
		a.Return = m.Actions.Return
	}
	return a
}

func holds(m *model.Match, req Request) bool {
	r := req.HTTP
	if len(m.Methods) > 0 && !containsFold(m.Methods, r.Method) {
		return false
	}
	if m.ClientIP != nil {
		in := false
		if req.ClientIP.IsValid() {
			addr := req.ClientIP.Unmap()
			for _, n := range m.ClientIP.Networks {
				if n.Contains(addr) {
					in = true
					break
				}
			}
		}
		if in == m.ClientIP.Not { // listed but "not" wanted, or unlisted but "in" wanted
			return false
		}
	}
	if m.Scheme != "" && !strings.EqualFold(m.Scheme, req.Scheme) {
		return false
	}
	if len(m.Hosts) > 0 && !hostMatches(m.Hosts, r.Host) {
		return false
	}
	for _, p := range m.Headers {
		if !patternHolds(p, r.Header.Get(p.Name)) {
			return false
		}
	}
	for _, p := range m.Query {
		if !patternHolds(p, r.URL.Query().Get(p.Name)) {
			return false
		}
	}
	for _, p := range m.Cookies {
		v := ""
		if c, err := r.Cookie(p.Name); err == nil {
			v = c.Value
		}
		if !patternHolds(p, v) {
			return false
		}
	}
	return true
}

// patternHolds tests a header, query parameter or cookie value. A missing
// value is treated as the empty string, so "!~" conditions hold for it.
func patternHolds(p model.Pattern, value string) bool {
	switch p.Op {
	case "~", "~*":
		return p.Regexp != nil && p.Regexp.MatchString(value)
	case "!~", "!~*":
		return p.Regexp == nil || !p.Regexp.MatchString(value)
	default:
		return value == p.Value
	}
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// hostMatches compares the request's host with the listed names. A name that
// starts with "*." matches any subdomain, like the site names do.
func hostMatches(hosts []string, requestHost string) bool {
	h, ok := vhost.NormalizeHost(requestHost)
	if !ok {
		return false
	}
	for _, want := range hosts {
		if want == h {
			return true
		}
		if strings.HasPrefix(want, "*.") && strings.HasSuffix(h, want[1:]) && len(h) > len(want)-1 {
			return true
		}
	}
	return false
}
