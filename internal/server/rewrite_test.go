package server

import (
	"strings"
	"testing"

	"github.com/laikait/lotus/internal/config/model"
)

func rewrite(pattern, target, flag string) model.Rewrite {
	return model.Rewrite{Pattern: pattern, Regexp: mustRe(pattern), Target: target, Flag: flag}
}

func TestRewrites(t *testing.T) {
	routes := []model.Route{
		{Kind: model.RoutePrefix, Path: "/old/", Rewrites: []model.Rewrite{
			rewrite(`^/old/(.*)$`, "/new/{re.1}", model.RewriteLast),
		}},
		{Kind: model.RoutePrefix, Path: "/go/", Rewrites: []model.Rewrite{
			rewrite(`^/go/temp/(\w+)$`, "/dest/{re.1}", model.RewriteRedirect),
			rewrite(`^/go/perm/(\w+)$`, "/dest/{re.1}", model.RewritePermanent),
		}},
		{Kind: model.RoutePrefix, Path: "/order/", Rewrites: []model.Rewrite{
			rewrite(`^/order/a$`, "/new/a.txt", model.RewriteLast),
			rewrite(`^/order/.*$`, "/page.html", model.RewriteLast), // never reached for /order/a
		}},
		{Kind: model.RoutePrefix, Path: "/both/", Actions: ret(200, "return-ran"), Rewrites: []model.Rewrite{
			rewrite(`^/both/x$`, "/new/a.txt", ""),
		}},
		{Kind: model.RoutePrefix, Path: "/q/", Rewrites: []model.Rewrite{
			rewrite(`^/q/keep$`, "/echo", model.RewriteLast),
			rewrite(`^/q/replace$`, "/echo?b=2", model.RewriteLast),
		}},
		{Kind: model.RouteExact, Path: "/echo", Actions: ret(200, "q={query}")},
		{Kind: model.RoutePrefix, Path: "/loop", Rewrites: []model.Rewrite{rewrite(`^/loop$`, "/loop", model.RewriteLast)}},
		{Kind: model.RoutePrefix, Path: "/jump/", Rewrites: []model.Rewrite{rewrite(`^/jump/`, "@elsewhere", model.RewriteLast)}},
		{Kind: model.RoutePrefix, Path: "/seg/", Rewrites: []model.Rewrite{rewrite(`^/seg/(.*)$`, "/{re.1}", model.RewriteLast)}},
		{Kind: model.RoutePrefix, Path: "/"},
	}
	named := model.Route{Kind: model.RouteNamed, Path: "@elsewhere", Actions: ret(200, "named-ran")}
	_, base := routedSite(t, routes, named)

	text := func(path string) string {
		_, body := do(t, "GET", base+path, nil, "")
		return strings.TrimSpace(body)
	}

	if got := text("/old/a.txt"); got != "new-a" {
		t.Errorf("last: %q", got)
	}
	resp, _ := do(t, "GET", base+"/go/temp/abc", nil, "")
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "/dest/abc" {
		t.Errorf("redirect: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, _ = do(t, "GET", base+"/go/perm/abc", nil, "")
	if resp.StatusCode != 301 || resp.Header.Get("Location") != "/dest/abc" {
		t.Errorf("permanent: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got := text("/order/a"); got != "new-a" {
		t.Errorf("the first matching rewrite must win: %q", got)
	}
	if got := text("/order/zzz"); got != "page" {
		t.Errorf("second rewrite: %q", got)
	}
	if got := text("/both/x"); got != "new-a" {
		t.Errorf("a matching rewrite runs before return: %q", got)
	}
	if got := text("/both/y"); got != "return-ran" {
		t.Errorf("without a match the route's own action runs: %q", got)
	}
	if got := text("/q/keep?a=1"); got != "q=a=1" {
		t.Errorf("no '?' in the target keeps the query: %q", got)
	}
	if got := text("/q/replace?a=1"); got != "q=b=2" {
		t.Errorf("'?' in the target replaces the query: %q", got)
	}
	if got := text("/jump/x"); got != "named-ran" {
		t.Errorf("rewrite to a named route: %q", got)
	}
	resp, _ = do(t, "GET", base+"/loop", nil, "")
	if resp.StatusCode != 500 {
		t.Errorf("rewrite loop: %d, want 500", resp.StatusCode)
	}
}

// A request path is decoded once, however many times it is rewritten.
func TestRewriteDoesNotDecodeTwice(t *testing.T) {
	routes := []model.Route{
		{Kind: model.RoutePrefix, Path: "/seg/", Rewrites: []model.Rewrite{rewrite(`^/seg/(.*)$`, "/{re.1}", model.RewriteLast)}},
		{Kind: model.RoutePrefix, Path: "/"},
	}
	_, base := routedSite(t, routes)

	// %20 is a space: "sp ace.txt" exists.
	if _, body := do(t, "GET", base+"/seg/sp%20ace.txt", nil, ""); body != "spaced" {
		t.Errorf("an encoded space: %q", body)
	}
	// %2520 is the text "%20", not a space: there is no file called "sp%20ace.txt".
	if resp, _ := do(t, "GET", base+"/seg/sp%2520ace.txt", nil, ""); resp.StatusCode != 404 {
		t.Errorf("a double-encoded path must not be decoded again: %d", resp.StatusCode)
	}
	// An encoded '?' stays part of the path; it does not start a query.
	routes = []model.Route{
		{Kind: model.RouteExact, Path: "/echo", Actions: ret(200, "q={query}")},
		{Kind: model.RoutePrefix, Path: "/seg/", Rewrites: []model.Rewrite{rewrite(`^/seg/(.*)$`, "/{re.1}", model.RewriteLast)}},
		{Kind: model.RoutePrefix, Path: "/"},
	}
	_, base = routedSite(t, routes)
	if resp, _ := do(t, "GET", base+"/seg/echo%3Fx=1", nil, ""); resp.StatusCode == 200 {
		t.Errorf("%%3F must not turn into a query separator")
	}
}

func TestTryFilesWithEncodedNames(t *testing.T) {
	routes := []model.Route{{Kind: model.RoutePrefix, Path: "/", Actions: model.Actions{TryFiles: []string{"{uri}", "=404"}}}}
	_, base := routedSite(t, routes)
	if _, body := do(t, "GET", base+"/sp%20ace.txt", nil, ""); body != "spaced" {
		t.Errorf("{uri} must be the decoded path when looking for files: %q", body)
	}
	if _, body := do(t, "GET", base+"/sp%20ace.txt?v=2", nil, ""); body != "spaced" {
		t.Errorf("a query must not disturb the file lookup: %q", body)
	}
	// {uri}/ finds a directory even when the request has a query.
	if _, body := do(t, "GET", base+"/blog?x=1", nil, ""); body == "" {
		t.Log("directory without slash redirects (expected)")
	}
}

func TestRewriteRejectsHeaderInjection(t *testing.T) {
	routes := []model.Route{
		{Kind: model.RoutePrefix, Path: "/r/", Rewrites: []model.Rewrite{rewrite(`(?s)^/r/(.*)$`, "/x/{re.1}", model.RewritePermanent)}},
		{Kind: model.RoutePrefix, Path: "/"},
	}
	_, base := routedSite(t, routes)
	resp, _ := do(t, "GET", base+"/r/a%0d%0aSet-Cookie:%20evil=1", nil, "")
	if resp.StatusCode != 400 || resp.Header.Get("Set-Cookie") != "" || resp.Header.Get("Location") != "" {
		t.Errorf("status %d, headers %v", resp.StatusCode, resp.Header)
	}
}

func TestErrorPages(t *testing.T) {
	pages := func(s *model.Site) {
		s.ErrorPages = map[int]string{404: "/404.html", 403: "@forbidden", 410: "/404.html"}
	}
	routes := []model.Route{
		{Kind: model.RouteExact, Path: "/gone", Actions: ret(410, "")},
		{Kind: model.RouteExact, Path: "/gone-text", Actions: ret(410, "explicit text")},
		{Kind: model.RouteExact, Path: "/tf", Actions: model.Actions{TryFiles: []string{"{uri}", "=404"}}},
		{Kind: model.RoutePrefix, Path: "/api/", ErrorPages: map[int]string{404: "/403.html"}}, // replaces the site's page for 404
		{Kind: model.RoutePrefix, Path: "/"},
	}
	named := model.Route{Kind: model.RouteNamed, Path: "@forbidden", Actions: ret(200, "named-error-page")}
	_, base := routedSiteWith(t, pages, routes, named)

	for _, tc := range []struct {
		name, method, path string
		hdr                map[string]string
		status             int
		body               string
	}{
		{"missing file", "GET", "/missing", nil, 404, "custom-404"},
		{"HEAD keeps the status", "HEAD", "/missing", nil, 404, ""},
		{"POST to a file is a 405, which has no page", "POST", "/missing", nil, 405, "method not allowed"},
		{"bare return", "GET", "/gone", nil, 410, "custom-404"},
		{"return with text is final", "GET", "/gone-text", nil, 410, "explicit text"},
		{"try_files =404", "GET", "/tf", nil, 404, "custom-404"},
		{"named route as error page", "GET", "/.env", nil, 403, "named-error-page"},
		{"route replaces the site's page", "GET", "/api/missing", nil, 404, "custom-403"},
		{"a page that exists is not an error", "GET", "/page.html", nil, 200, "page"},
		{"a condition on the original request doesn't turn the page into 304", "GET", "/missing",
			map[string]string{"If-None-Match": "*", "If-Modified-Since": "Mon, 02 Jan 2090 15:04:05 GMT"}, 404, "custom-404"},
		{"a range on the original request doesn't cut the page", "GET", "/missing",
			map[string]string{"Range": "bytes=0-2"}, 404, "custom-404"},
	} {
		resp, body := do(t, tc.method, base+tc.path, tc.hdr, "")
		if resp.StatusCode != tc.status || strings.TrimSpace(body) != tc.body {
			t.Errorf("%s: %s %s = %d %q, want %d %q", tc.name, tc.method, tc.path, resp.StatusCode, strings.TrimSpace(body), tc.status, tc.body)
		}
	}

	// The page's own headers are the page's, not the error's.
	resp, _ := do(t, "GET", base+"/missing", nil, "")
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want the page's (text/html)", ct)
	}
	if resp.Header.Get("X-Content-Type-Options") == "nosniff" && resp.Header.Get("Content-Type") == "text/plain; charset=utf-8" {
		t.Errorf("headers of the replaced error leaked into the page: %v", resp.Header)
	}
}

func TestErrorPageThatFailsIsNotReplacedAgain(t *testing.T) {
	pages := func(s *model.Site) { s.ErrorPages = map[int]string{404: "/no-such-page.html"} }
	h, base := routedSiteWith(t, pages, []model.Route{{Kind: model.RoutePrefix, Path: "/"}})
	resp, body := do(t, "GET", base+"/missing", nil, "")
	if resp.StatusCode != 404 || !strings.Contains(body, "404") {
		t.Errorf("%d %q", resp.StatusCode, body)
	}
	if strings.Contains(h.stderr.String(), "LOTUS-RT-0001") {
		t.Error("a missing error page must not loop")
	}
}

func TestErrorPageNamedRouteMissing(t *testing.T) {
	pages := func(s *model.Site) { s.ErrorPages = map[int]string{404: "@nope"} }
	h, base := routedSiteWith(t, pages, []model.Route{{Kind: model.RoutePrefix, Path: "/"}})
	if resp, _ := do(t, "GET", base+"/missing", nil, ""); resp.StatusCode != 500 {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
	if !strings.Contains(h.stderr.String(), "@nope") {
		t.Errorf("error log: %q", h.stderr.String())
	}
}

func TestAccessLogShowsTheFinalErrorStatus(t *testing.T) {
	pages := func(s *model.Site) { s.ErrorPages = map[int]string{404: "/404.html"} }
	h, base := routedSiteWith(t, pages, []model.Route{{Kind: model.RoutePrefix, Path: "/"}})
	do(t, "GET", base+"/missing", nil, "")
	if log := waitForLog(h, `"GET /missing HTTP/1.1" 404 10`); !strings.Contains(log, `"GET /missing HTTP/1.1" 404 10`) {
		t.Errorf("want status 404 and the page's 10 bytes in the log:\n%s", log)
	}
}
