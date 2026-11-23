package server

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/router"
	"github.com/laikait/lotus/internal/staticfiles"
)

// maxInternalRedirects bounds try_files fallbacks, rewrites, named-route jumps
// and error pages for one request (docs/architecture/routing-precedence.md §5).
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

// errorState is what a request carries once an error page has taken over.
type errorState struct {
	inPage bool // an error page is being served: further errors are not replaced
	force  int  // the status the page's response must carry
}

// serve runs the routing engine for one request and writes the response.
func (rt *siteRuntime) serve(w http.ResponseWriter, r *http.Request, clientIP netip.Addr) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	origBody := r.Body
	reqID := newRequestID()

	var es errorState
	cur := target{path: router.NormalizePath(r.URL.Path), query: r.URL.RawQuery}
	for hops := 0; ; hops++ {
		if hops > maxInternalRedirects {
			rt.log.Error("too many internal redirects", "code", "LOTUS-RT-0001", "uri", r.RequestURI)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		req := withTarget(r, cur)
		if es.inPage {
			asErrorPageRequest(req)
		}

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
			pages   = rt.site.ErrorPages
		)
		if route != nil {
			m := router.SelectMatch(route, router.Request{HTTP: req, ClientIP: clientIP, Scheme: scheme})
			actions = router.Effective(route, m)
			files = rt.filesFor(route)
			maxBody = route.MaxRequestBody
			pages = route.ErrorPages
		}

		// out is where this step writes. When error pages apply it intercepts the
		// responses they replace.
		var (
			out http.ResponseWriter = w
			ew  *errorWriter
		)
		if es.force != 0 || (!es.inPage && len(pages) > 0) {
			ew = newErrorWriter(w, pages, es)
			out = ew
		}

		vars := &router.Vars{
			URI:       router.EscapedURI(cur.path, cur.query),
			Path:      cur.path,
			Query:     cur.query,
			Host:      req.Host,
			Scheme:    scheme,
			Method:    req.Method,
			RemoteIP:  addrString(clientIP, r),
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

		// One step: it writes a response, or says where to route next.
		step := func() (next target, again bool) {
			if es.inPage {
				req.Body, req.ContentLength = http.NoBody, 0
			} else {
				if req.ContentLength > maxBody {
					http.Error(out, "request body too large", http.StatusRequestEntityTooLarge)
					return target{}, false
				}
				req.Body = http.MaxBytesReader(w, origBody, maxBody)
			}

			// Rewrites run first; the first one that matches decides.
			if route != nil {
				for i := range route.Rewrites {
					rw := &route.Rewrites[i]
					m := rw.Regexp.FindStringSubmatch(cur.path)
					if m == nil {
						continue
					}
					rv := *vars
					rv.Captures = m
					if rw.Flag != model.RewriteRedirect && rw.Flag != model.RewritePermanent {
						path, query, hasQuery, err := expandInternal(rw.Target, &rv)
						if err != nil {
							http.Error(out, "bad request", http.StatusBadRequest)
							return target{}, false
						}
						return rt.nextTarget(out, req, path, query, hasQuery)
					}
					text, err := router.Expand(rw.Target, &rv)
					if err != nil {
						http.Error(out, "bad request", http.StatusBadRequest)
						return target{}, false
					}
					status := http.StatusFound
					if rw.Flag == model.RewritePermanent {
						status = http.StatusMovedPermanently
					}
					if text == "" {
						rt.log.Error("rewrite: a redirect needs a target", "pattern", rw.Pattern)
						http.Error(out, "internal server error", http.StatusInternalServerError)
						return target{}, false
					}
					w.Header().Set("Location", text)
					w.WriteHeader(status)
					return target{}, false
				}
			}

			switch {
			case actions.Return != nil:
				// An explicit body is final and skips error pages, but while an
				// error page is being served it must still carry the original status.
				final := http.ResponseWriter(w)
				if es.force != 0 {
					final = out
				}
				rt.respond(final, out, req, actions.Return, vars)
				return target{}, false

			case len(actions.TryFiles) > 0:
				return rt.tryFiles(out, req, actions.TryFiles, files, vars)

			default:
				if files == nil {
					http.Error(out, "404 page not found", http.StatusNotFound)
					return target{}, false
				}
				files.ServeHTTP(out, req)
				return target{}, false
			}
		}
		next, again := step()
		if again {
			cur = next
			continue
		}

		// An error response was replaced: route to its page, remembering the status.
		if ew != nil && ew.intercepted {
			es = errorState{inPage: true, force: ew.code}
			page := pages[ew.code]
			if strings.HasPrefix(page, "@") {
				named := router.FindNamed(rt.site.Named, page)
				if named == nil {
					rt.log.Error("error_page: no such named route", "route", page)
					http.Error(w, "internal server error", http.StatusInternalServerError)
					return
				}
				cur = target{path: cur.path, query: cur.query, named: named}
			} else {
				cur = target{path: router.NormalizePath(page), query: cur.query}
			}
			continue
		}
		return
	}
}

// asErrorPageRequest turns req into what an error page is served for: a plain
// GET (or HEAD), with the conditions and ranges of the original request gone.
func asErrorPageRequest(req *http.Request) {
	if req.Method != http.MethodHead {
		req.Method = http.MethodGet
	}
	for _, h := range []string{"Range", "If-Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since"} {
		req.Header.Del(h)
	}
}

// expandInternal expands a template that names a URI to route to internally
// (a try_files entry or a rewrite target). The template is split at its first
// '?' before anything is expanded: the path part expands to decoded text, with
// {uri} meaning the path alone, and the query part expands to raw query text.
// Nothing is percent-decoded afterwards, so a path such as /a%2520b is never
// decoded twice, and a '?' that came from a request value can't start a query.
func expandInternal(tmpl string, v *router.Vars) (path, query string, hasQuery bool, err error) {
	pathT, queryT, hasQuery := strings.Cut(tmpl, "?")
	pv := *v
	pv.URI = v.Path
	if path, err = router.Expand(pathT, &pv); err != nil {
		return "", "", false, err
	}
	if hasQuery {
		if query, err = router.Expand(queryT, v); err != nil {
			return "", "", false, err
		}
	}
	return path, query, hasQuery, nil
}

// nextTarget decides where routing continues, given the expanded path (or
// "@name") and query of an internal redirect. Without a query part the
// original query stays. again is false when a response was already written.
func (rt *siteRuntime) nextTarget(w http.ResponseWriter, r *http.Request, path, query string, hasQuery bool) (next target, again bool) {
	if strings.HasPrefix(path, "@") {
		named := router.FindNamed(rt.site.Named, path)
		if named == nil {
			rt.log.Error("no such named route", "route", path)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return target{}, false
		}
		return target{path: router.NormalizePath(r.URL.Path), query: r.URL.RawQuery, named: named}, true
	}
	if !hasQuery {
		query = r.URL.RawQuery
	}
	return target{path: router.NormalizePath(path), query: query}, true
}

// tryFiles serves the first candidate that exists. If none does, it says where
// to route next, or writes the final "=NNN" status itself.
func (rt *siteRuntime) tryFiles(w http.ResponseWriter, r *http.Request, list []string,
	files *staticfiles.Handler, vars *router.Vars) (next target, again bool) {

	last := len(list) - 1
	for _, cand := range list[:last] {
		expanded, _, _, err := expandInternal(cand, vars)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return target{}, false
		}
		if files == nil {
			continue
		}
		p := router.NormalizePath(expanded)
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
		return target{}, false
	}

	fallback, query, hasQuery, err := expandInternal(list[last], vars)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return target{}, false
	}
	if strings.HasPrefix(fallback, "=") {
		code, err := strconv.Atoi(fallback[1:])
		if err != nil || code < 200 || code > 599 {
			rt.log.Error("try_files: bad status in last entry", "entry", fallback)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return target{}, false
		}
		rt.status(w, r, code, "")
		return target{}, false
	}
	return rt.nextTarget(w, r, fallback, query, hasQuery)
}

// respond answers a `return` action. A response with an explicit body (text)
// is final and goes straight to w; a bare status goes through out, so an
// error page can replace it.
func (rt *siteRuntime) respond(w, out http.ResponseWriter, r *http.Request, ret *model.Return, vars *router.Vars) {
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
		if text == "" {
			rt.status(out, r, ret.Status, "")
		} else {
			rt.status(w, r, ret.Status, text)
		}
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

// addrString formats the client address, or what the connection reports when
// there is no IP (a Unix-socket peer).
func addrString(a netip.Addr, r *http.Request) string {
	if a.IsValid() {
		return a.String()
	}
	return remoteIP(r)
}

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
