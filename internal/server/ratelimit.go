package server

import (
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/ratelimit"
)

// Request rate limits (docs/architecture/limits.md).
//
// A site can have a limit that counts all its requests, and each route one of
// its own; a request is checked against both. Clients are told apart by their
// resolved address (after trusted_proxies), IPv6 clients by /64. A request over
// the limit gets 429 with Retry-After and goes no further: it reads no file,
// runs no script, reaches no backend.

type rateLimit struct {
	l      *ratelimit.Limiter
	global bool
}

func newRateLimit(rl *model.RateLimit) *rateLimit {
	if rl == nil {
		return nil
	}
	return &rateLimit{
		l:      ratelimit.NewLimiter(rl.Requests, time.Duration(rl.Per), rl.Burst),
		global: rl.Key == model.RateKeyGlobal,
	}
}

func (r *rateLimit) allow(ip netip.Addr, now time.Time) (bool, time.Duration) {
	key := "*"
	if !r.global {
		if key = ratelimit.Key(ip); key == "" {
			key = "local" // a peer without an IP address, such as a Unix socket
		}
	}
	return r.l.Allow(key, now)
}

// routeLimits finds the routes' limits, by route.
func routeLimits(routes []model.Route, into map[*model.Route]*rateLimit) {
	for i := range routes {
		r := &routes[i]
		if l := newRateLimit(r.RateLimit); l != nil {
			into[r] = l
		}
		routeLimits(r.Children, into)
	}
}

// rateCheck charges the request to the site's limit and the route's. charged
// remembers what this request already paid, so routing again (an internal
// redirect) doesn't count it twice. It reports whether the request is refused
// and how long the client should wait.
func (rt *siteRuntime) rateCheck(route *model.Route, ip netip.Addr, charged *[]*rateLimit) (bool, time.Duration) {
	now := time.Now()
	for _, l := range [2]*rateLimit{rt.rate, rt.routeRates[route]} {
		if l == nil {
			continue
		}
		seen := false
		for _, c := range *charged {
			seen = seen || c == l
		}
		if seen {
			continue
		}
		*charged = append(*charged, l)
		if ok, wait := l.allow(ip, now); !ok {
			return true, wait
		}
	}
	return false, 0
}

// retrySeconds formats a Retry-After value: whole seconds, at least 1.
func retrySeconds(d time.Duration) string {
	s := int64((d + time.Second - 1) / time.Second)
	if s < 1 {
		s = 1
	}
	return strconv.FormatInt(s, 10)
}

// tooManyRequests answers a refused request.
func (rt *siteRuntime) tooManyRequests(w http.ResponseWriter, r *http.Request, wait time.Duration) {
	rt.stats.rateLimited.Add(1)
	w.Header().Set("Retry-After", retrySeconds(wait))
	rt.status(w, r, http.StatusTooManyRequests, "")
}
