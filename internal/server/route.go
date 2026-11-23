package server

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/router"
	"github.com/laikait/lotus/internal/staticfiles"
)

// maxInternalRedirects bounds try_files fallbacks and named-route jumps for one
// request (docs/architecture/routing-precedence.md §5).
const maxInternalRedirects = 10

func filesKey(root string, index []string) string {
	return root + "\x00" + strings.Join(index, "\x00")
}

// filesFor returns the static handler for a route's effective root, or nil if
// the route has none.
func (rt *siteRuntime) filesFor(route *model.Route) *staticfiles.Handler {
	if route == nil || route.Root == "" {
		return nil
	}
	return rt.handlers[filesKey(route.Root, route.Index)]
}

// target is where routing currently points: a path and query, or a named route.
type target struct {
	path  string
	query string
	named *model.Route
}

// serve runs the routing engine for one request and writes the response.
func (rt *siteRuntime) serve(w http.ResponseWriter, r *http.Request) {
	clientIP, _ := netip.ParseAddr(remoteIP(r))
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	origBody := r.Body
	reqID := newRequestID()

	cur := target{path: router.NormalizePath(r.URL.Path), query: r.URL.RawQuery}
	for hops := 0; ; hops++ {
		if hops > maxInternalRedirects {
			rt.log.Error("too many internal redirects", "code", "LOTUS-RT-0001", "uri", r.RequestURI)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		req := withTarget(r, cur)

		var (
			route *model.Route
			caps  []string
		)
		if cur.named != nil {
			route = cur.named
		} else if f, ok := router.Find(rt.site.Routes, cur.path); ok {
			route, caps = f.Route, f.Captures
		}

		var (
			actions model.Actions
			files   = rt.defaultFiles
			maxBody = rt.maxBody
		)
		if route != nil {
			m := router.SelectMatch(route, router.Request{HTTP: req, ClientIP: clientIP, Scheme: scheme})
			actions = router.Effective(route, m)
			files = rt.filesFor(route)
			maxBody = route.MaxRequestBody
		}

		if req.ContentLength > maxBody {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		req.Body = http.MaxBytesReader(w, origBody, maxBody)

		vars := &router.Vars{
			URI:       router.EscapedURI(cur.path, cur.query),
			Path:      cur.path,
			Query:     cur.query,
			Host:      req.Host,
			Scheme:    scheme,
			Method:    req.Method,
			RemoteIP:  remoteIP(r),
			RequestID: reqID,
			Header:    func(n string) string { return req.Header.Get(n) },
			QueryArg:  func(n string) string { return req.URL.Query().Get(n) },
			Cookie: func(n string) string {
				if c, err := req.Cookie(n); err == nil {
					return c.Value
				}
				return ""
			},
			Captures: caps,
		}

		switch {
		case actions.Return != nil:
			rt.respond(w, req, actions.Return, vars)
			return

		case len(actions.TryFiles) > 0:
			next, handled := rt.tryFiles(w, req, actions.TryFiles, files, vars)
			if handled {
				return
			}
			cur = next
			continue

		default:
			if files == nil {
				http.Error(w, "404 page not found", http.StatusNotFound)
				return
			}
			files.ServeHTTP(w, req)
			return
		}
	}
}

// tryFiles serves the first candidate that exists. If none does, it returns
// where to route next, or handles the final "=NNN" status itself.
func (rt *siteRuntime) tryFiles(w http.ResponseWriter, r *http.Request, list []string,
	files *staticfiles.Handler, vars *router.Vars) (next target, handled bool) {

	last := len(list) - 1
	for _, cand := range list[:last] {
		expanded, err := router.Expand(cand, vars)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return target{}, true
		}
		if files == nil {
			continue
		}
		p, _, _ := strings.Cut(expanded, "?")
		p = router.NormalizePath(p)
		isDir, exists := files.Stat(p)
		if !exists {
			continue
		}
		// "x/" names a directory and "x" names a file, as in Nginx.
		if isDir != strings.HasSuffix(expanded, "/") {
			continue
		}
		r2 := withTarget(r, target{path: p, query: r.URL.RawQuery})
		files.ServeHTTP(w, r2)
		return target{}, true
	}

	fallback, err := router.Expand(list[last], vars)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return target{}, true
	}
	switch {
	case strings.HasPrefix(fallback, "="):
		code, err := strconv.Atoi(fallback[1:])
		if err != nil || code < 200 || code > 599 {
			rt.log.Error("try_files: bad status in last entry", "entry", fallback)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return target{}, true
		}
		rt.status(w, r, code, "")
		return target{}, true

	case strings.HasPrefix(fallback, "@"):
		named := router.FindNamed(rt.site.Named, fallback)
		if named == nil {
			rt.log.Error("try_files: no such named route", "route", fallback)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return target{}, true
		}
		return target{path: router.NormalizePath(r.URL.Path), query: r.URL.RawQuery, named: named}, false
	}

	// A URI: re-route. A '?' in it replaces the query; otherwise the original stays.
	p, q, hasQuery := strings.Cut(fallback, "?")
	if !hasQuery {
		q = r.URL.RawQuery
	}
	if dec, err := url.PathUnescape(p); err == nil {
		p = dec
	}
	return target{path: router.NormalizePath(p), query: q}, false
}

// respond answers a `return` action.
func (rt *siteRuntime) respond(w http.ResponseWriter, r *http.Request, ret *model.Return, vars *router.Vars) {
	if ret.Status < 200 {
		rt.log.Error("return: informational status codes can't be a final response", "status", ret.Status)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	text, err := router.Expand(ret.Target, vars)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	switch ret.Status {
	case 301, 302, 303, 307, 308:
		if text == "" {
			rt.log.Error("return: a redirect needs a target", "status", ret.Status)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Location", text)
		w.WriteHeader(ret.Status)
	default:
		rt.status(w, r, ret.Status, text)
	}
}

// status writes a plain-text response. An empty body gets the standard status text.
func (rt *siteRuntime) status(w http.ResponseWriter, r *http.Request, code int, body string) {
	if body == "" && bodyAllowed(code) {
		body = http.StatusText(code)
	}
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	if bodyAllowed(code) && r.Method != http.MethodHead {
		_, _ = io.WriteString(w, body+"\n")
	}
}

func bodyAllowed(code int) bool { return code != 204 && code != 304 }

// withTarget returns a copy of r pointing at the target's path and query. The
// copy is what handlers see, so the original (used for the access log) keeps
// the URI the client sent.
func withTarget(r *http.Request, t target) *http.Request {
	r2 := r.Clone(r.Context())
	r2.URL.Path = t.path
	r2.URL.RawPath = ""
	r2.URL.RawQuery = t.query
	return r2
}

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
