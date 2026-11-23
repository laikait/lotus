package server

import (
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/explain"
)

// The explain command must predict what the server really does. This test
// starts a server on a rich configuration, sends many requests, and compares
// the status, the redirect target and the body with explain's answer.
func TestExplainAgreesWithTheServer(t *testing.T) {
	routes := []model.Route{
		{Kind: model.RouteExact, Path: "/old", Actions: ret(301, "/new")},
		{Kind: model.RouteExact, Path: "/gone", Actions: ret(410, "")},
		{Kind: model.RouteExact, Path: "/teapot", Actions: ret(418, "short and stout")},
		{Kind: model.RouteExact, Path: "/echo", Actions: ret(200, "q={query} ip={remote_ip}")},
		{Kind: model.RouteRegex, Path: `^/u/(\d+)$`, Regexp: mustRe(`^/u/(\d+)$`), Actions: ret(302, "/user?id={re.1}")},
		{Kind: model.RoutePrefix, Path: "/blog/", Rewrites: []model.Rewrite{
			rewrite(`^/blog/\d{4}/([a-z]+)$`, "/posts/{re.1}.html", model.RewriteLast),
			rewrite(`^/blog/old/(.*)$`, "/new/{re.1}", model.RewritePermanent),
		}},
		{Kind: model.RoutePrefix, Path: "/app/", Actions: model.Actions{TryFiles: []string{"{uri}", "@app"}}},
		{Kind: model.RoutePrefix, Path: "/api/", Actions: model.Actions{TryFiles: []string{"{uri}", "=404"}},
			ErrorPages: map[int]string{404: "/403.html"}},
		{Kind: model.RoutePrefix, Path: "/admin/", Actions: ret(200, "admin"), Matches: []model.Match{
			{Methods: []string{"POST", "DELETE"}, Actions: ret(405, "read only")},
			{Headers: []model.Pattern{{Name: "X-Block", Op: "=", Value: "1"}}, Actions: ret(403, "")},
		}},
		{Kind: model.RouteFinalPrefix, Path: "/static/", Actions: model.Actions{TryFiles: []string{"{uri}", "=404"}}},
		{Kind: model.RouteRegexFold, Path: `\.TXT$`, Regexp: mustRe(`(?i)\.txt$`), Actions: ret(200, "txt-route")},
		{Kind: model.RoutePrefix, Path: "/loop", Rewrites: []model.Rewrite{rewrite(`^/loop$`, "/loop", model.RewriteLast)}},
		{Kind: model.RoutePrefix, Path: "/", Actions: model.Actions{TryFiles: []string{"{uri}", "{uri}/", "=404"}}},
	}
	named := model.Route{Kind: model.RouteNamed, Path: "@app", Actions: ret(200, "app-shell")}
	pages := func(s *model.Site) { s.ErrorPages = map[int]string{404: "/404.html", 410: "/404.html"} }
	cfg := routedConfig(t, pages, routes, named)
	root := cfg.Sites[0].Root
	must(t, os.MkdirAll(filepath.Join(root, "posts"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "posts", "hello.html"), []byte("post-hello"), 0o644))
	must(t, os.MkdirAll(filepath.Join(root, "static"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "static", "a.css"), []byte("css"), 0o644))

	h := start(t, cfg)
	base := "http://" + h.addr("http")

	files := explain.NewDiskFiles()
	defer files.Close()

	type probe struct {
		method, path string
		hdr          map[string]string
	}
	probes := []probe{
		{"GET", "/", nil}, {"GET", "/index.html", nil}, {"GET", "/hello.txt", nil}, {"GET", "/HELLO.TXT", nil},
		{"GET", "/missing", nil}, {"GET", "/missing.txt", nil}, {"GET", "/.env", nil}, {"HEAD", "/missing", nil},
		{"GET", "/blog", nil}, {"GET", "/blog/", nil}, {"GET", "/blog/2024/hello", nil}, {"GET", "/blog/2024/nothing", nil},
		{"GET", "/blog/old/x?y=1", nil},
		{"GET", "/old", nil}, {"POST", "/old", nil}, {"GET", "/gone", nil}, {"GET", "/teapot", nil},
		{"GET", "/echo?a=b", nil}, {"GET", "/u/42", nil}, {"GET", "/u/abc", nil},
		{"GET", "/app/missing", nil}, {"GET", "/app/index.html", nil},
		{"GET", "/api/missing", nil}, {"GET", "/api/x", nil},
		{"GET", "/admin/", nil}, {"POST", "/admin/", nil}, {"GET", "/admin/", map[string]string{"X-Block": "1"}},
		{"GET", "/static/a.css", nil}, {"GET", "/static/missing.txt", nil},
		{"GET", "/loop", nil}, {"GET", "/a/../hello.txt", nil}, {"GET", "//hello.txt", nil},
		{"GET", "/sp%20ace.txt", nil}, {"GET", "/page.html?x=1", nil}, {"DELETE", "/hello.txt", nil},
	}
	for _, p := range probes {
		resp, body := do(t, p.method, base+p.path, p.hdr, "")

		u, _ := splitTarget(p.path)
		hdr := http.Header{}
		for k, v := range p.hdr {
			hdr.Set(k, v)
		}
		rep := explain.Explain(cfg, explain.Request{
			Method: p.method, Host: "a.test", Path: u.path, RawQuery: u.query, Header: hdr,
			ClientIP: netip.MustParseAddr("127.0.0.1"),
		}, files)

		out := rep.Outcome
		label := p.method + " " + p.path
		if out.Kind == explain.KindDepends {
			t.Errorf("%s: explain could not decide with files available", label)
			continue
		}
		if resp.StatusCode != out.Status {
			t.Errorf("%s: the server answered %d, explain says %d (%s)\n%s", label, resp.StatusCode, out.Status, out.Summary(), dump(rep))
			continue
		}
		if out.Kind == explain.KindRedirect {
			if got := resp.Header.Get("Location"); got != out.Location {
				t.Errorf("%s: Location %q, explain says %q", label, got, out.Location)
			}
		}
		if out.Kind == explain.KindStatus && p.method != "HEAD" && out.Status != 204 {
			// Explicit text, or the standard text of the status (static errors
			// carry their own message in Note).
			want := out.Body
			if want == "" {
				want = out.Note
			}
			if want == "" {
				want = http.StatusText(out.Status)
			}
			if strings.TrimSpace(body) != want {
				t.Errorf("%s: body %q, explain says %q", label, strings.TrimSpace(body), want)
			}
		}
		if out.Kind == explain.KindFile && p.method == "GET" {
			want, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(out.File)))
			if err != nil || string(want) != body {
				t.Errorf("%s: body %q, but explain says the file %s holds %q (%v)", label, body, out.File, want, err)
			}
		}
	}
}

type splitURL struct{ path, query string }

func splitTarget(p string) (splitURL, bool) {
	path, query, _ := strings.Cut(p, "?")
	// the request line carries the encoded form; explain wants the decoded path
	path = strings.ReplaceAll(path, "%20", " ")
	return splitURL{path, query}, true
}

func dump(rep *explain.Report) string {
	var b strings.Builder
	explain.WriteText(&b, rep)
	return b.String()
}
