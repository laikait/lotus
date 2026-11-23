// Package explain answers "what would LOTUS do with this request?" without
// serving anything. It follows the same steps as the server (virtual host,
// route, match block, rewrite, try_files, error page, static file), using the
// same router and static-file code, and records each decision.
//
// It never sends a request, never writes, and reads the file system only to
// check whether files exist.
package explain

import (
	"fmt"
	"net/http"
	"net/netip"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/router"
	"github.com/laikait/lotus/internal/staticfiles"
	"github.com/laikait/lotus/internal/vhost"
)

// maxInternalRedirects must equal the server's limit.
const maxInternalRedirects = 10

// Request describes the request to explain.
type Request struct {
	Method   string
	Host     string // Host header; the port is ignored
	Path     string // decoded request path
	RawQuery string
	Scheme   string // "http" or "https"
	Header   http.Header
	// ClientIP is the client address after trusted_proxies were applied. It is
	// invalid when unknown; client_ip conditions then see an unknown address.
	ClientIP netip.Addr
	// Listener restricts the lookup to one listener. Empty: the first listener
	// that has a site for the host.
	Listener string
}

// Step is one decision on the way to the answer.
type Step struct {
	Phase  string `json:"phase"`
	Detail string `json:"detail"`
}

// Outcome kinds.
const (
	KindFile     = "file"     // a file is served
	KindPHP      = "php"      // a PHP script is run
	KindStatus   = "status"   // an error or fixed response
	KindRedirect = "redirect" // 301, 302, ...
	KindDepends  = "depends"  // can't be known without checking files
	KindError    = "error"    // a configuration or runtime problem (e.g. a loop)
)

// Outcome is the final answer.
type Outcome struct {
	Kind     string `json:"kind"`
	Status   int    `json:"status,omitempty"`
	File     string `json:"file,omitempty"`     // URL path of the file or script, for KindFile and KindPHP
	Location string `json:"location,omitempty"` // for KindRedirect
	Body     string `json:"body,omitempty"`     // explicit text of a return
	Note     string `json:"note,omitempty"`
}

// Report is the complete explanation.
type Report struct {
	Listener string  `json:"listener,omitempty"`
	Site     string  `json:"site,omitempty"`
	Steps    []Step  `json:"steps"`
	Outcome  Outcome `json:"outcome"`
}

// Files gives access to document roots so that try_files and static serving
// can be checked. A nil Files means "don't check": answers that depend on
// whether a file exists are reported as KindDepends.
type Files interface {
	Handler(site *model.Site, root string, index []string) (*staticfiles.Handler, error)
}

type explainer struct {
	cfg     *model.Config
	req     Request
	files   Files
	site    *model.Site
	steps   []Step
	phpSite bool // some route of the site runs PHP
	// speculative is set when the answer assumes files that weren't checked don't exist.
	speculative bool
}

func (e *explainer) step(phase, format string, args ...any) {
	e.steps = append(e.steps, Step{Phase: phase, Detail: fmt.Sprintf(format, args...)})
}

// Explain follows req through cfg.
func Explain(cfg *model.Config, req Request, files Files) *Report {
	e := &explainer{cfg: cfg, req: req, files: files}
	if e.req.Method == "" {
		e.req.Method = http.MethodGet
	}
	if e.req.Scheme == "" {
		e.req.Scheme = "http"
	}
	if e.req.Header == nil {
		e.req.Header = http.Header{}
	}
	rep := &Report{}
	if !e.chooseSite(rep) {
		rep.Steps = e.steps
		return rep
	}
	e.phpSite = e.site.UsesPHP()
	if e.site.HTAccess != nil {
		e.step("htaccess", "this site reads .htaccess files; explain does not apply them, so rewrites, redirects, access rules, error documents and headers they define are not reflected below")
	}
	rep.Outcome = e.route()
	rep.Steps = e.steps
	return rep
}

// chooseSite picks the listener and site, as the server's virtual host lookup does.
func (e *explainer) chooseSite(rep *Report) bool {
	host, ok := vhost.NormalizeHost(e.req.Host)
	if !ok {
		e.step("host", "the Host header %q is not a valid hostname", e.req.Host)
		rep.Outcome = Outcome{Kind: KindStatus, Status: http.StatusBadRequest, Note: "invalid Host header"}
		return false
	}

	type candidate struct {
		listener string
		site     *model.Site
		matched  bool
	}
	var chosen *candidate
	var fallback *candidate
	for _, l := range e.cfg.Listeners {
		if e.req.Listener != "" && l.Name != e.req.Listener {
			continue
		}
		var entries []vhost.Entry[*model.Site]
		for i := range e.cfg.Sites {
			s := &e.cfg.Sites[i]
			if containsString(s.Listeners, l.Name) {
				entries = append(entries, vhost.Entry[*model.Site]{Value: s, Hosts: append([]string{s.Name}, s.Aliases...)})
			}
		}
		site, matched, ok := vhost.Build(entries).Lookup(host)
		if !ok {
			continue
		}
		c := &candidate{l.Name, site, matched}
		if matched {
			chosen = c
			break
		}
		if fallback == nil {
			fallback = c
		}
	}
	if chosen == nil {
		chosen = fallback
	}
	if chosen == nil {
		if e.req.Listener != "" {
			e.step("host", "no site is attached to listener %q", e.req.Listener)
		} else {
			e.step("host", "no listener has a site")
		}
		rep.Outcome = Outcome{Kind: KindStatus, Status: http.StatusNotFound, Note: "no site is configured for this listener"}
		return false
	}
	e.site = chosen.site
	rep.Listener, rep.Site = chosen.listener, chosen.site.Name
	if chosen.matched {
		e.step("host", "Host %q selects site %q on listener %q", host, chosen.site.Name, chosen.listener)
	} else {
		e.step("host", "Host %q matches no site on listener %q; its default site %q answers", host, chosen.listener, chosen.site.Name)
	}
	return true
}

type target struct {
	path, query string
	named       *model.Route
}

// route runs the routing loop (the same as the server's serve).
func (e *explainer) route() Outcome {
	var (
		inPage bool
		force  int
		cur    = target{path: router.NormalizePath(e.req.Path), query: e.req.RawQuery}
	)
	if cur.path != e.req.Path {
		e.step("path", "the path %q is normalized to %q before routing", e.req.Path, cur.path)
	}

	for hops := 0; ; hops++ {
		if hops > maxInternalRedirects {
			e.step("limit", "more than %d internal redirects", maxInternalRedirects)
			return Outcome{Kind: KindError, Status: 500, Note: "too many internal redirects (LOTUS-RT-0001)"}
		}

		httpReq := e.httpRequest(cur, inPage)

		var (
			route *model.Route
			caps  []string
		)
		switch {
		case cur.named != nil:
			route = cur.named
			e.step("route", "named route %s", route.Path)
		default:
			if f, ok := router.Find(e.site.Routes, cur.path); ok {
				route, caps = f.Route, f.Captures
				e.step("route", "%s for path %q", describeRoute(route), cur.path)
			} else {
				e.step("route", "no route matches %q; the site's default handler serves files from the site root", cur.path)
			}
		}

		var (
			actions model.Actions
			root    = e.site.Root
			index   = e.site.Index
			pages   = e.site.ErrorPages
		)
		if route != nil {
			root, index, pages = route.Root, route.Index, route.ErrorPages
			if len(route.Matches) > 0 {
				m := router.SelectMatch(route, router.Request{HTTP: httpReq, ClientIP: e.req.ClientIP, Scheme: e.req.Scheme})
				if m == nil {
					e.step("match", "none of the %d match block(s) applies; the route's own actions run", len(route.Matches))
				} else {
					for i := range route.Matches {
						if &route.Matches[i] == m {
							e.step("match", "match block #%d applies; its actions replace the route's", i+1)
						}
					}
				}
				actions = router.Effective(route, m)
			} else {
				actions = route.Actions
			}
		}

		vars := e.vars(httpReq, cur, caps)

		out, next, again := e.action(route, actions, root, index, vars, cur)
		if again {
			cur = next
			continue
		}

		// An error response that has an error page is replaced by it.
		if !inPage && out.Kind == KindStatus && out.Body == "" {
			if page, ok := pages[out.Status]; ok {
				e.step("error_page", "status %d has the error page %q; it is served with status %d", out.Status, page, out.Status)
				inPage, force = true, out.Status
				if strings.HasPrefix(page, "@") {
					named := router.FindNamed(e.site.Named, page)
					if named == nil {
						e.step("error_page", "there is no named route %s", page)
						return Outcome{Kind: KindError, Status: 500, Note: "error_page names a route that doesn't exist"}
					}
					cur = target{path: cur.path, query: cur.query, named: named}
				} else {
					cur = target{path: router.NormalizePath(page), query: cur.query}
				}
				continue
			}
		}
		if force != 0 && out.Status == http.StatusOK {
			out.Status = force
			out.Note = strings.TrimSpace(out.Note + " the original status is kept by error_page")
		}
		if e.speculative && out.Kind != KindDepends {
			out.Note = strings.TrimSpace(out.Note + " assuming none of the unchecked files exist")
		}
		return out
	}
}

// action decides what one routing step does.
func (e *explainer) action(route *model.Route, a model.Actions, root string, index []string,
	vars *router.Vars, cur target) (out Outcome, next target, again bool) {

	// Rewrites first.
	if route != nil {
		for i := range route.Rewrites {
			rw := &route.Rewrites[i]
			m := rw.Regexp.FindStringSubmatch(cur.path)
			if m == nil {
				continue
			}
			rv := *vars
			rv.Captures = m
			if rw.Flag == model.RewriteRedirect || rw.Flag == model.RewritePermanent {
				text, err := router.Expand(rw.Target, &rv)
				if err != nil {
					return unsafeValue(e), target{}, false
				}
				status := http.StatusFound
				if rw.Flag == model.RewritePermanent {
					status = http.StatusMovedPermanently
				}
				e.step("rewrite", "rewrite #%d (%s) matches; %s redirect to %q", i+1, rw.Pattern, rw.Flag, text)
				return Outcome{Kind: KindRedirect, Status: status, Location: text}, target{}, false
			}
			path, query, hasQuery, err := router.ExpandInternal(rw.Target, &rv)
			if err != nil {
				return unsafeValue(e), target{}, false
			}
			e.step("rewrite", "rewrite #%d (%s) matches; routing starts again with %q", i+1, rw.Pattern, describeURI(path, query, hasQuery))
			return e.nextTarget(path, query, hasQuery, cur)
		}
		if len(route.Rewrites) > 0 {
			e.step("rewrite", "none of the %d rewrite(s) matches", len(route.Rewrites))
		}
	}

	switch {
	case a.Return != nil:
		return e.doReturn(a.Return, vars), target{}, false

	case len(a.TryFiles) > 0:
		return e.tryFiles(a, root, index, vars, cur)

	}
	return e.static(root, index, cur, a)
}

func unsafeValue(e *explainer) Outcome {
	e.step("expand", "a placeholder value contains a CR, LF or NUL character; the request is refused")
	return Outcome{Kind: KindStatus, Status: http.StatusBadRequest, Note: "unsafe placeholder value"}
}

func (e *explainer) doReturn(ret *model.Return, vars *router.Vars) Outcome {
	text, err := router.Expand(ret.Target, vars)
	if err != nil {
		return unsafeValue(e)
	}
	switch ret.Status {
	case 301, 302, 303, 307, 308:
		e.step("return", "return %d: redirect to %q", ret.Status, text)
		return Outcome{Kind: KindRedirect, Status: ret.Status, Location: text}
	}
	if text == "" {
		e.step("return", "return %d (no text)", ret.Status)
	} else {
		e.step("return", "return %d with explicit text (final: error pages don't replace it)", ret.Status)
	}
	return Outcome{Kind: KindStatus, Status: ret.Status, Body: text}
}

func (e *explainer) tryFiles(a model.Actions, root string, index []string, vars *router.Vars, cur target) (Outcome, target, bool) {
	list := a.TryFiles
	h, note := e.handler(root, index)
	last := len(list) - 1
	for _, cand := range list[:last] {
		path, _, _, err := router.ExpandInternal(cand, vars)
		if err != nil {
			return unsafeValue(e), target{}, false
		}
		if h == nil {
			e.step("try_files", "candidate %q (%s)", path, note)
			continue
		}
		p := router.NormalizePath(path)
		isDir, exists := h.Stat(p)
		switch {
		case !exists:
			e.step("try_files", "candidate %q does not exist", p)
		case isDir != strings.HasSuffix(path, "/"):
			e.step("try_files", "candidate %q exists but is a %s", p, map[bool]string{true: "directory", false: "file"}[isDir])
		default:
			e.step("try_files", "candidate %q exists; it is served", p)
			return e.serveFile(h, p, a, cur)
		}
	}
	if h == nil && last > 0 {
		e.step("try_files", "which candidate is served depends on the files; the last entry applies if none exists")
	}
	path, query, hasQuery, err := router.ExpandInternal(list[last], vars)
	if err != nil {
		return unsafeValue(e), target{}, false
	}
	if strings.HasPrefix(path, "=") {
		code, err := strconv.Atoi(path[1:])
		if err != nil || code < 200 || code > 599 {
			e.step("try_files", "the last entry %q is not a valid status", path)
			return Outcome{Kind: KindError, Status: 500, Note: "bad status in try_files"}, target{}, false
		}
		e.step("try_files", "last entry: status %d", code)
		if h == nil && last > 0 {
			e.speculative = true
		}
		return Outcome{Kind: KindStatus, Status: code}, target{}, false
	}
	e.step("try_files", "last entry: route again with %q", describeURI(path, query, hasQuery))
	if h == nil && last > 0 {
		// Keep going: this is what happens when no candidate exists.
		e.speculative = true
		e.step("note", "following the fallback, as if no candidate existed")
	}
	return e.nextTarget(path, query, hasQuery, cur)
}

func (e *explainer) nextTarget(path, query string, hasQuery bool, cur target) (Outcome, target, bool) {
	if strings.HasPrefix(path, "@") {
		named := router.FindNamed(e.site.Named, path)
		if named == nil {
			e.step("route", "there is no named route %s", path)
			return Outcome{Kind: KindError, Status: 500, Note: "no such named route"}, target{}, false
		}
		return Outcome{}, target{path: cur.path, query: cur.query, named: named}, true
	}
	if !hasQuery {
		query = cur.query
	}
	return Outcome{}, target{path: router.NormalizePath(path), query: query}, true
}

func (e *explainer) static(root string, index []string, cur target, a model.Actions) (Outcome, target, bool) {
	if root == "" {
		e.step("static", "the site has no document root")
		return Outcome{Kind: KindStatus, Status: http.StatusNotFound, Note: "no document root"}, target{}, false
	}
	h, note := e.handler(root, index)
	if h == nil {
		what := "serves"
		if a.PHP != "" && !a.PHPDeny {
			what = "serves or runs"
		}
		e.step("static", "%s %q from %s (%s)", what, cur.path, root, note)
		return Outcome{Kind: KindDepends, Note: "depends on the files in " + root}, target{}, false
	}
	return e.serveFile(h, cur.path, a, cur)
}

func isPHPName(name string) bool { return strings.EqualFold(path.Ext(name), ".php") }

// serveFile mirrors the server's serveFiles: which of static serving and PHP
// applies to a file request, and the rules that keep PHP source from being sent.
func (e *explainer) serveFile(h *staticfiles.Handler, p string, a model.Actions, cur target) (Outcome, target, bool) {
	switch {
	case a.PHPDeny:
		return e.describeFile(h, p, isPHPName, "php_deny: a .php file is never run or served here"), target{}, false
	case a.PHP != "":
		return e.describePHP(h, p, a.PHP), target{}, false
	case e.phpSite:
		if strings.HasSuffix(p, "/") {
			if o := h.Locate(p, nil); o.Status == http.StatusOK && isPHPName(o.File) {
				e.step("php", "%q is a directory whose index is %q; routing starts again with it so the route that runs PHP gets it", p, o.File)
				return Outcome{}, target{path: o.File, query: cur.query}, true
			}
		}
		return e.describeFile(h, p, isPHPName, "this route does not run PHP, so a .php file is refused rather than served as text"), target{}, false
	}
	return e.describeFile(h, p, nil, ""), target{}, false
}

// describePHP is what the server's servePHP decides for a path.
func (e *explainer) describePHP(h *staticfiles.Handler, p, backend string) Outcome {
	if e.req.Method == http.MethodTrace {
		e.step("php", "TRACE is refused")
		return Outcome{Kind: KindStatus, Status: http.StatusMethodNotAllowed}
	}
	script, info, found := splitScript(p)
	if !found {
		if o := h.Locate(p, nil); o.Status == http.StatusOK && isPHPName(o.File) {
			script, found = o.File, true
		}
	}
	if !found {
		e.step("php", "%q is not a script (no .php file); the static file rules apply", p)
		return e.describeFile(h, p, isPHPName, "")
	}
	o := h.Locate(script, nil)
	if o.Status != http.StatusOK || o.File != path.Clean(script) {
		status := http.StatusNotFound
		if o.Status == http.StatusForbidden || o.Status == http.StatusBadRequest {
			status = o.Status
		}
		e.step("php", "the script %q does not exist or is not allowed; PHP is not run", script)
		return Outcome{Kind: KindStatus, Status: status}
	}
	note := "backend " + backend
	if info != "" {
		note += ", PATH_INFO " + info
	}
	e.step("php", "runs the script %q on php_backend %q", o.File, backend)
	return Outcome{Kind: KindPHP, Status: 200, File: o.File, Note: note}
}

// splitScript must match the server's: the first segment ending in .php ends the script's path.
func splitScript(p string) (script, info string, ok bool) {
	for i := 0; i < len(p); {
		end := len(p)
		if j := strings.IndexByte(p[i+1:], '/'); j >= 0 {
			end = i + 1 + j
		}
		if isPHPName(p[i+1 : end]) {
			return p[:end], p[end:], true
		}
		i = end
	}
	return "", "", false
}

func (e *explainer) describeFile(h *staticfiles.Handler, p string, refuse func(string) bool, why string) Outcome {
	o := h.DescribeWith(e.req.Method, p, refuse)
	if why != "" && o.Status == http.StatusForbidden && o.Message == "forbidden" {
		e.step("php", "%s", why)
	}
	switch {
	case o.Status == http.StatusOK:
		e.step("static", "%q is the file %q", p, o.File)
		return Outcome{Kind: KindFile, Status: 200, File: o.File}
	case o.Redirect != "":
		e.step("static", "%q is a directory; redirects to %q", p, o.Redirect)
		return Outcome{Kind: KindRedirect, Status: o.Status, Location: o.Redirect}
	}
	e.step("static", "%q gives %d (%s)", p, o.Status, o.Message)
	return Outcome{Kind: KindStatus, Status: o.Status, Note: o.Message}
}

// handler returns the static handler for a root, or nil and the reason.
func (e *explainer) handler(root string, index []string) (*staticfiles.Handler, string) {
	if root == "" {
		return nil, "no document root"
	}
	if e.files == nil {
		return nil, "files not checked"
	}
	h, err := e.files.Handler(e.site, root, index)
	if err != nil || h == nil {
		return nil, "document root not readable here"
	}
	return h, ""
}

func (e *explainer) httpRequest(cur target, inPage bool) *http.Request {
	r, _ := http.NewRequest(e.req.Method, "http://"+e.req.Host+"/", nil)
	r.Host = e.req.Host
	r.Header = e.req.Header.Clone()
	r.URL.Path, r.URL.RawQuery = cur.path, cur.query
	if inPage {
		if r.Method != http.MethodHead {
			r.Method = http.MethodGet
		}
		for _, h := range []string{"Range", "If-Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since"} {
			r.Header.Del(h)
		}
	}
	return r
}

func (e *explainer) vars(r *http.Request, cur target, caps []string) *router.Vars {
	remote := ""
	if e.req.ClientIP.IsValid() {
		remote = e.req.ClientIP.String()
	}
	return &router.Vars{
		URI: router.EscapedURI(cur.path, cur.query), Path: cur.path, Query: cur.query,
		Host: r.Host, Scheme: e.req.Scheme, Method: r.Method, RemoteIP: remote, RequestID: "<request-id>",
		Header:   func(n string) string { return r.Header.Get(n) },
		QueryArg: func(n string) string { return r.URL.Query().Get(n) },
		Cookie: func(n string) string {
			if c, err := r.Cookie(n); err == nil {
				return c.Value
			}
			return ""
		},
		Captures: caps,
	}
}

func describeRoute(r *model.Route) string {
	switch r.Kind {
	case model.RouteExact:
		return fmt.Sprintf("exact route = %q", r.Path)
	case model.RoutePrefix:
		return fmt.Sprintf("prefix route %q", r.Path)
	case model.RouteFinalPrefix:
		return fmt.Sprintf("final prefix route ^~ %q", r.Path)
	case model.RouteRegex:
		return fmt.Sprintf("regex route ~ '%s'", r.Path)
	case model.RouteRegexFold:
		return fmt.Sprintf("regex route ~* '%s'", r.Path)
	}
	return string(r.Kind) + " route " + r.Path
}

func describeURI(path, query string, hasQuery bool) string {
	if hasQuery {
		return path + "?" + query
	}
	return path
}

func containsString(list []string, s string) bool { return slices.Contains(list, s) }
