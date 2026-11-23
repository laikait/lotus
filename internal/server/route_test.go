package server

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/config/model"
)

// routedSite builds a site on a document root that also has a "blog"
// directory, and attaches the given routes.
func routedSite(t *testing.T, routes []model.Route, named ...model.Route) (*harness, string) {
	return routedSiteWith(t, nil, routes, named...)
}

// routedSiteWith is routedSite with a hook to change the site first.
func routedSiteWith(t *testing.T, mutate func(*model.Site), routes []model.Route, named ...model.Route) (*harness, string) {
	t.Helper()
	root := docroot(t, "home")
	must(t, os.MkdirAll(filepath.Join(root, "blog"), 0o755))
	must(t, os.MkdirAll(filepath.Join(root, "new"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "blog", "index.html"), []byte("blog-index"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "page.html"), []byte("page"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "new", "a.txt"), []byte("new-a"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "sp ace.txt"), []byte("spaced"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "404.html"), []byte("custom-404"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "403.html"), []byte("custom-403"), 0o644))
	s := siteConf("a.test", root, "http")
	s.Limits.MaxRequestBody = model.DefaultMaxRequestBody
	if mutate != nil {
		mutate(&s)
	}
	inherit(routes, &s)
	inherit(named, &s)
	s.Routes, s.Named = routes, named
	h := start(t, baseConfig(s))
	return h, "http://" + h.addr("http")
}

// inherit fills in what the compiler would: a route without its own root,
// index or body limit takes the site's.
func inherit(routes []model.Route, s *model.Site) {
	for i := range routes {
		r := &routes[i]
		if r.Root == "" {
			r.Root = s.Root
		}
		if r.Index == nil {
			r.Index = s.Index
		}
		if r.MaxRequestBody == 0 {
			r.MaxRequestBody = s.Limits.MaxRequestBody
		}
		merged := map[int]string{}
		for k, v := range s.ErrorPages {
			merged[k] = v
		}
		for k, v := range r.ErrorPages {
			merged[k] = v
		}
		r.ErrorPages = merged
		inherit(r.Children, s)
	}
}

// do sends a request without following redirects.
func do(t *testing.T, method, url string, hdr map[string]string, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	must(t, err)
	req.Host = "a.test"
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	c := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	must(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func ret(status int, target string) model.Actions {
	return model.Actions{Return: &model.Return{Status: status, Target: target}}
}

func TestReturnAndRedirects(t *testing.T) {
	routes := []model.Route{
		{Kind: model.RouteExact, Path: "/old", Actions: ret(301, "/new")},
		{Kind: model.RouteExact, Path: "/echo", Actions: ret(302, "/dest?from={path}&q={query}")},
		{Kind: model.RouteExact, Path: "/gone", Actions: ret(410, "it is gone")},
		{Kind: model.RouteExact, Path: "/empty", Actions: ret(204, "")},
		{Kind: model.RouteExact, Path: "/literal", Actions: ret(200, "\x00{uri} stays")},
		{Kind: model.RouteRegex, Path: `^/u/(\d+)$`, Regexp: mustRe(`^/u/(\d+)$`), Actions: ret(301, "/user?id={re.1}")},
		{Kind: model.RoutePrefix, Path: "/", MaxRequestBody: model.DefaultMaxRequestBody},
	}
	_, base := routedSite(t, routes)

	resp, _ := do(t, "GET", base+"/old", nil, "")
	if resp.StatusCode != 301 || resp.Header.Get("Location") != "/new" {
		t.Errorf("/old: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, _ = do(t, "GET", base+"/echo?a=1", nil, "")
	if resp.Header.Get("Location") != "/dest?from=/echo&q=a=1" {
		t.Errorf("/echo: Location = %q", resp.Header.Get("Location"))
	}
	resp, body := do(t, "GET", base+"/gone", nil, "")
	if resp.StatusCode != 410 || strings.TrimSpace(body) != "it is gone" {
		t.Errorf("/gone: %d %q", resp.StatusCode, body)
	}
	resp, body = do(t, "GET", base+"/empty", nil, "")
	if resp.StatusCode != 204 || body != "" {
		t.Errorf("/empty: %d %q", resp.StatusCode, body)
	}
	_, body = do(t, "GET", base+"/literal", nil, "")
	if strings.TrimSpace(body) != "{uri} stays" {
		t.Errorf("an escaped brace must stay literal: %q", body)
	}
	resp, _ = do(t, "GET", base+"/u/42", nil, "")
	if resp.Header.Get("Location") != "/user?id=42" {
		t.Errorf("regex capture: Location = %q", resp.Header.Get("Location"))
	}
	resp, body = do(t, "HEAD", base+"/gone", nil, "")
	if resp.StatusCode != 410 || body != "" {
		t.Errorf("HEAD must not carry a body: %d %q", resp.StatusCode, body)
	}
	resp, _ = do(t, "POST", base+"/old", nil, "x")
	if resp.StatusCode != 301 {
		t.Errorf("return applies to every method: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Server"); got != "lotus" {
		t.Errorf("Server = %q", got)
	}
}

func TestReturnRejectsHeaderInjection(t *testing.T) {
	routes := []model.Route{{Kind: model.RoutePrefix, Path: "/", Actions: ret(302, "/x{path}")}}
	_, base := routedSite(t, routes)
	resp, _ := do(t, "GET", base+"/a%0d%0aSet-Cookie:%20evil=1", nil, "")
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if resp.Header.Get("Set-Cookie") != "" || resp.Header.Get("Location") != "" {
		t.Errorf("injected header reached the client: %v", resp.Header)
	}
}

func TestTryFiles(t *testing.T) {
	routes := []model.Route{
		{Kind: model.RoutePrefix, Path: "/", Actions: model.Actions{TryFiles: []string{"{uri}", "{uri}/", "/index.html"}}},
		{Kind: model.RoutePrefix, Path: "/api/", Actions: model.Actions{TryFiles: []string{"{uri}", "=404"}}},
		{Kind: model.RoutePrefix, Path: "/app/", Actions: model.Actions{TryFiles: []string{"{uri}", "@fallback"}}},
	}
	named := model.Route{Kind: model.RouteNamed, Path: "@fallback", Actions: ret(200, "fallback-ran")}
	_, base := routedSite(t, routes, named)

	for _, tc := range []struct {
		path, want string
		status     int
	}{
		{"/hello.txt", "hello from home", 200}, // {uri} is a file
		{"/blog/", "blog-index", 200},          // {uri} is a directory, skipped; {uri}/ matches
		{"/page.html", "page", 200},
		{"/nope/deep", "home", 200}, // nothing exists: re-route to /index.html
		{"/.env", "home", 200},      // a forbidden file counts as missing and reveals nothing
		{"/api/missing", "Not Found", 404},
		{"/app/missing", "fallback-ran", 200},
		{"/@fallback", "home", 200}, // a client can't reach a named route
	} {
		resp, body := do(t, "GET", base+tc.path, nil, "")
		if resp.StatusCode != tc.status || strings.TrimSpace(body) != tc.want {
			t.Errorf("GET %s = %d %q, want %d %q", tc.path, resp.StatusCode, strings.TrimSpace(body), tc.status, tc.want)
		}
	}

	// /blog without the slash: {uri} is a directory, so {uri}/ serves it.
	resp, body := do(t, "GET", base+"/blog", nil, "")
	if resp.StatusCode != 200 || body != "blog-index" {
		t.Errorf("/blog = %d %q", resp.StatusCode, body)
	}
}

func TestTryFilesQueryHandling(t *testing.T) {
	routes := []model.Route{
		{Kind: model.RouteExact, Path: "/keep", Actions: model.Actions{TryFiles: []string{"/target"}}},
		{Kind: model.RouteExact, Path: "/replace", Actions: model.Actions{TryFiles: []string{"/target?b=2"}}},
		{Kind: model.RouteExact, Path: "/target", Actions: ret(200, "q={query}")},
		{Kind: model.RoutePrefix, Path: "/"},
	}
	_, base := routedSite(t, routes)
	for path, want := range map[string]string{
		"/keep?a=1":    "q=a=1", // no '?' in the new URI: the original query stays
		"/replace?a=1": "q=b=2", // a '?' in the new URI replaces it
	} {
		if _, body := do(t, "GET", base+path, nil, ""); strings.TrimSpace(body) != want {
			t.Errorf("GET %s = %q, want %q", path, body, want)
		}
	}
}

func TestInternalRedirectLimit(t *testing.T) {
	routes := []model.Route{{Kind: model.RoutePrefix, Path: "/", Actions: model.Actions{TryFiles: []string{"/loop"}}}}
	h, base := routedSite(t, routes)
	resp, _ := do(t, "GET", base+"/x", nil, "")
	if resp.StatusCode != 500 {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	if !strings.Contains(h.stderr.String(), "LOTUS-RT-0001") {
		t.Errorf("error log lacks LOTUS-RT-0001: %q", h.stderr.String())
	}
}

func TestMissingNamedRouteIsAnError(t *testing.T) {
	routes := []model.Route{{Kind: model.RoutePrefix, Path: "/", Actions: model.Actions{TryFiles: []string{"@missing"}}}}
	h, base := routedSite(t, routes)
	if resp, _ := do(t, "GET", base+"/x", nil, ""); resp.StatusCode != 500 {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
	if !strings.Contains(h.stderr.String(), "@missing") {
		t.Errorf("the error log should name the route: %q", h.stderr.String())
	}
}

func TestMatchBlocksAtRuntime(t *testing.T) {
	routes := []model.Route{{
		Kind: model.RoutePrefix, Path: "/admin/", Actions: ret(200, "admin"),
		Matches: []model.Match{
			{Methods: []string{"POST", "DELETE"}, Actions: ret(405, "no writes")},
			{Headers: []model.Pattern{{Name: "X-Block", Op: "=", Value: "yes"}}, Actions: ret(403, "blocked")},
			{ClientIP: &model.ClientIPCond{Not: true, Networks: []netipPrefix{mustPrefix("10.0.0.0/8")}},
				Query: []model.Pattern{{Name: "from", Op: "=", Value: "outside"}}, Actions: ret(401, "login")},
		},
	}, {Kind: model.RoutePrefix, Path: "/"}}
	_, base := routedSite(t, routes)

	cases := []struct {
		name, method, path string
		hdr                map[string]string
		status             int
	}{
		{"no match applies", "GET", "/admin/", nil, 200},
		{"method match", "POST", "/admin/", nil, 405},
		{"header match", "GET", "/admin/", map[string]string{"X-Block": "yes"}, 403},
		{"first matching block wins", "POST", "/admin/", map[string]string{"X-Block": "yes"}, 405},
		{"all conditions must hold (query alone)", "GET", "/admin/?from=outside", nil, 401}, // 127.0.0.1 is not in 10/8
		{"query miss", "GET", "/admin/?from=inside", nil, 200},
	}
	for _, tc := range cases {
		resp, _ := do(t, tc.method, base+tc.path, tc.hdr, "")
		if resp.StatusCode != tc.status {
			t.Errorf("%s: %s %s = %d, want %d", tc.name, tc.method, tc.path, resp.StatusCode, tc.status)
		}
	}
}

func TestRoutePrecedenceThroughServer(t *testing.T) {
	routes := []model.Route{
		{Kind: model.RoutePrefix, Path: "/"},
		{Kind: model.RouteFinalPrefix, Path: "/static/", Actions: ret(200, "static-final")},
		{Kind: model.RouteRegexFold, Path: `\.txt$`, Regexp: mustRe(`(?i)\.txt$`), Actions: ret(200, "regex-txt")},
		{Kind: model.RouteExact, Path: "/hello.txt", Actions: ret(200, "exact")},
	}
	_, base := routedSite(t, routes)
	for path, want := range map[string]string{
		"/hello.txt":       "exact",        // exact beats regex
		"/other.TXT":       "regex-txt",    // regex beats the plain prefix "/"
		"/static/a.txt":    "static-final", // a final prefix stops regex routes
		"/page.html":       "page",         // falls to the "/" prefix: static files
		"/a//../hello.txt": "exact",        // the path is normalized before matching
		"//hello.txt":      "exact",
	} {
		if _, body := do(t, "GET", base+path, nil, ""); strings.TrimSpace(body) != want {
			t.Errorf("GET %s = %q, want %q", path, strings.TrimSpace(body), want)
		}
	}
}

func TestRouteRootOverride(t *testing.T) {
	other := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(other, "files"), 0o755))
	must(t, os.WriteFile(filepath.Join(other, "files", "a.txt"), []byte("from-other-root"), 0o644))

	routes := []model.Route{
		{Kind: model.RoutePrefix, Path: "/files/", Root: other, Index: []string{"index.html"}, MaxRequestBody: model.DefaultMaxRequestBody},
		{Kind: model.RoutePrefix, Path: "/", MaxRequestBody: model.DefaultMaxRequestBody},
	}
	_, base := routedSite(t, routes)
	if _, body := do(t, "GET", base+"/files/a.txt", nil, ""); body != "from-other-root" {
		t.Errorf("route root: %q", body)
	}
	// The other root has no /page.html, and the site root's file is out of reach there.
	if resp, _ := do(t, "GET", base+"/files/page.html", nil, ""); resp.StatusCode != 404 {
		t.Errorf("the site root must not leak into a route with its own root: %d", resp.StatusCode)
	}
	if _, body := do(t, "GET", base+"/page.html", nil, ""); body != "page" {
		t.Errorf("site root: %q", body)
	}
}

func TestPerRouteBodyLimit(t *testing.T) {
	routes := []model.Route{
		{Kind: model.RoutePrefix, Path: "/small/", MaxRequestBody: 5},
		{Kind: model.RoutePrefix, Path: "/", MaxRequestBody: model.DefaultMaxRequestBody},
	}
	_, base := routedSite(t, routes)
	if resp, _ := do(t, "POST", base+"/small/x", nil, "0123456789"); resp.StatusCode != 413 {
		t.Errorf("over the route's limit: %d, want 413", resp.StatusCode)
	}
	// Under the limit the request reaches the static handler, which refuses POST.
	if resp, _ := do(t, "POST", base+"/small/x", nil, "012"); resp.StatusCode != 405 {
		t.Errorf("under the limit: %d, want 405", resp.StatusCode)
	}
	if resp, _ := do(t, "POST", base+"/other", nil, "0123456789"); resp.StatusCode != 405 {
		t.Errorf("the site default is larger: %d, want 405", resp.StatusCode)
	}
}

func TestAccessLogKeepsTheOriginalURI(t *testing.T) {
	routes := []model.Route{{Kind: model.RoutePrefix, Path: "/", Actions: model.Actions{TryFiles: []string{"{uri}", "/index.html"}}}}
	h, base := routedSite(t, routes)
	do(t, "GET", base+"/nope?x=1", nil, "")
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(h.stdout.String(), `"GET /nope?x=1 HTTP/1.1" 200`) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := h.stdout.String(); !strings.Contains(got, `"GET /nope?x=1 HTTP/1.1" 200`) {
		t.Errorf("access log = %q", got)
	}
}
