package explain

import (
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/config/model"
)

func site(name string, aliases []string, listeners ...string) model.Site {
	return model.Site{Name: name, Aliases: aliases, Listeners: listeners, Index: []string{"index.html"}}
}

func cfgWith(sites ...model.Site) *model.Config {
	return &model.Config{
		Listeners: []model.Listener{{Name: "web"}, {Name: "admin"}},
		Sites:     sites,
	}
}

func ret(status int, target string) model.Actions {
	return model.Actions{Return: &model.Return{Status: status, Target: target}}
}

func TestSiteSelection(t *testing.T) {
	cfg := cfgWith(
		site("a.test", []string{"*.a.test"}, "web"),
		site("b.test", nil, "web"),
		site("admin.test", nil, "admin"),
	)
	tests := []struct {
		name, host, listener, wantSite, wantListener string
		wantStatus                                   int
	}{
		{"exact", "b.test", "", "b.test", "web", 0},
		{"port and case are ignored", "B.TEST:8080", "", "b.test", "web", 0},
		{"wildcard alias", "shop.a.test", "", "a.test", "web", 0},
		{"another listener", "admin.test", "", "admin.test", "admin", 0},
		{"unknown host gets the default site", "nope.test", "web", "a.test", "web", 0},
		{"listener restricts the search", "b.test", "admin", "admin.test", "admin", 0},
		{"invalid host", "a b", "", "", "", 400},
	}
	for _, tt := range tests {
		rep := Explain(cfg, Request{Host: tt.host, Path: "/", Listener: tt.listener}, nil)
		if tt.wantStatus != 0 {
			if rep.Outcome.Status != tt.wantStatus {
				t.Errorf("%s: outcome %+v", tt.name, rep.Outcome)
			}
			continue
		}
		if rep.Site != tt.wantSite || rep.Listener != tt.wantListener {
			t.Errorf("%s: site %q on %q, want %q on %q", tt.name, rep.Site, rep.Listener, tt.wantSite, tt.wantListener)
		}
	}
	rep := Explain(cfg, Request{Host: "x.test", Path: "/", Listener: "nonexistent"}, nil)
	if rep.Outcome.Status != 404 {
		t.Errorf("an unknown listener: %+v", rep.Outcome)
	}
}

func TestRoutingDecisionsWithoutFiles(t *testing.T) {
	s := site("a.test", nil, "web")
	s.Root = "/srv/a"
	s.Routes = []model.Route{
		{Kind: model.RouteExact, Path: "/old", Actions: ret(301, "/new")},
		{Kind: model.RouteExact, Path: "/teapot", Actions: ret(418, "short and stout")},
		{Kind: model.RouteRegex, Path: `^/u/(\d+)$`, Regexp: regexp.MustCompile(`^/u/(\d+)$`), Actions: ret(302, "/user?id={re.1}")},
		{Kind: model.RoutePrefix, Path: "/blog/", Rewrites: []model.Rewrite{
			{Pattern: `^/blog/(.*)$`, Regexp: regexp.MustCompile(`^/blog/(.*)$`), Target: "/posts/{re.1}", Flag: model.RewritePermanent},
		}},
		{Kind: model.RoutePrefix, Path: "/loop", Rewrites: []model.Rewrite{
			{Pattern: `^/loop$`, Regexp: regexp.MustCompile(`^/loop$`), Target: "/loop", Flag: model.RewriteLast},
		}},
		{Kind: model.RoutePrefix, Path: "/app/", Actions: model.Actions{TryFiles: []string{"{uri}", "@missing"}}},
		{Kind: model.RoutePrefix, Path: "/", Root: "/srv/a", Index: []string{"index.html"}},
	}
	cfg := cfgWith(s)

	tests := []struct {
		path       string
		kind       string
		status     int
		location   string
		noteHas    string
		stepsMatch string
	}{
		{"/old", KindRedirect, 301, "/new", "", "exact route"},
		{"/teapot", KindStatus, 418, "", "", ""},
		{"/u/42", KindRedirect, 302, "/user?id=42", "", "regex route"},
		{"/blog/hello", KindRedirect, 301, "/posts/hello", "", "rewrite #1"},
		{"/loop", KindError, 500, "", "LOTUS-RT-0001", ""},
		{"/app/x", KindError, 500, "", "no such named route", ""},
		{"/page.html", KindDepends, 0, "", "depends on the files", "files not checked"},
		{"/a/../old", KindRedirect, 301, "/new", "", "normalized"},
	}
	for _, tt := range tests {
		rep := Explain(cfg, Request{Host: "a.test", Path: tt.path}, nil)
		o := rep.Outcome
		if o.Kind != tt.kind || o.Status != tt.status || o.Location != tt.location || !strings.Contains(o.Note, tt.noteHas) {
			t.Errorf("%s: %+v, want kind %s status %d location %q note ~%q", tt.path, o, tt.kind, tt.status, tt.location, tt.noteHas)
		}
		var all strings.Builder
		for _, st := range rep.Steps {
			all.WriteString(st.Phase + " " + st.Detail + "\n")
		}
		if !strings.Contains(all.String(), tt.stepsMatch) {
			t.Errorf("%s: steps lack %q:\n%s", tt.path, tt.stepsMatch, all.String())
		}
	}
}

func TestMatchBlocksUseTheGivenClient(t *testing.T) {
	s := site("a.test", nil, "web")
	s.Routes = []model.Route{{
		Kind: model.RoutePrefix, Path: "/", Actions: ret(200, "ok"),
		Matches: []model.Match{
			{ClientIP: &model.ClientIPCond{Not: true, Networks: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}, Actions: ret(403, "outside")},
			{Methods: []string{"POST"}, Actions: ret(405, "no posts")},
		},
	}}
	cfg := cfgWith(s)
	ask := func(method, ip string, hdr http.Header) Outcome {
		r := Request{Method: method, Host: "a.test", Path: "/", Header: hdr}
		if ip != "" {
			r.ClientIP = netip.MustParseAddr(ip)
		}
		return Explain(cfg, r, nil).Outcome
	}
	if o := ask("GET", "10.1.1.1", nil); o.Body != "ok" {
		t.Errorf("inside: %+v", o)
	}
	if o := ask("GET", "8.8.8.8", nil); o.Status != 403 {
		t.Errorf("outside: %+v", o)
	}
	if o := ask("POST", "10.1.1.1", nil); o.Status != 405 {
		t.Errorf("second block: %+v", o)
	}
	// An unknown client counts as outside every network.
	if o := ask("GET", "", nil); o.Status != 403 {
		t.Errorf("unknown client: %+v", o)
	}
}

func TestErrorPageIsFollowedAndKeepsStatus(t *testing.T) {
	s := site("a.test", nil, "web")
	s.ErrorPages = map[int]string{410: "@gone"}
	s.Routes = []model.Route{
		{Kind: model.RouteExact, Path: "/x", Actions: ret(410, ""), ErrorPages: map[int]string{410: "@gone"}},
		{Kind: model.RouteExact, Path: "/y", Actions: ret(410, "explicit"), ErrorPages: map[int]string{410: "@gone"}},
	}
	s.Named = []model.Route{{Kind: model.RouteNamed, Path: "@gone", Actions: ret(200, "gone page"), ErrorPages: map[int]string{410: "@gone"}}}
	cfg := cfgWith(s)

	o := Explain(cfg, Request{Host: "a.test", Path: "/x"}, nil).Outcome
	if o.Status != 410 || o.Body != "gone page" {
		t.Errorf("bare status: %+v", o)
	}
	o = Explain(cfg, Request{Host: "a.test", Path: "/y"}, nil).Outcome
	if o.Status != 410 || o.Body != "explicit" {
		t.Errorf("explicit text is final: %+v", o)
	}
}

func TestSummaryAndText(t *testing.T) {
	rep := Explain(cfgWith(site("a.test", nil, "web")), Request{Host: "a.test", Path: "/"}, nil)
	var b strings.Builder
	WriteText(&b, rep)
	for _, want := range []string{"site: a.test (listener web)", "result:", "no route matches"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("text lacks %q:\n%s", want, b.String())
		}
	}
	if got := (Outcome{Kind: KindRedirect, Status: 301, Location: "/x"}).Summary(); got != "301 Moved Permanently, redirects to /x" {
		t.Errorf("summary = %q", got)
	}
}

func TestExplainSaysWhenHtaccessIsNotApplied(t *testing.T) {
	s := site("a.test", nil, "http")
	s.Root = "/srv/a"
	s.HTAccess = &model.HTAccess{}
	cfg := &model.Config{Listeners: []model.Listener{{Name: "http"}}, Sites: []model.Site{s}}
	rep := Explain(cfg, Request{Host: "a.test", Path: "/x"}, nil)
	found := false
	for _, st := range rep.Steps {
		found = found || (st.Phase == "htaccess" && strings.Contains(st.Detail, "does not apply"))
	}
	if !found {
		t.Errorf("no warning: %+v", rep.Steps)
	}
	// A site without .htaccess gets no such step.
	s.HTAccess = nil
	cfg.Sites[0] = s
	for _, st := range Explain(cfg, Request{Host: "a.test", Path: "/x"}, nil).Steps {
		if st.Phase == "htaccess" {
			t.Error("a warning for a site that doesn't read .htaccess")
		}
	}
}

func TestExplainDescribesProxyRoutes(t *testing.T) {
	s := site("a.test", nil, "web")
	s.Root = "/srv/a"
	s.Routes = []model.Route{
		{Kind: model.RoutePrefix, Path: "/api/", Actions: model.Actions{Proxy: "app", ProxyHeaders: []model.Header{{Name: "X-Env", Value: "prod"}, {Name: "X-Drop"}}}},
		{Kind: model.RoutePrefix, Path: "/gone/", Actions: model.Actions{Proxy: "missing"}},
	}
	cfg := cfgWith(s)
	cfg.Upstreams = []model.Upstream{{Name: "app", Balance: "least_conn", Backends: []model.UpstreamBackend{{Address: "http://h:1"}, {Address: "http://h:2"}}}}
	rep := Explain(cfg, Request{Host: "a.test", Path: "/api/users?id=7"}, nil)
	if rep.Outcome.Kind != KindProxy || !strings.Contains(rep.Outcome.Summary(), "proxied") {
		t.Fatalf("%+v", rep.Outcome)
	}
	var all strings.Builder
	for _, st := range rep.Steps {
		all.WriteString(st.Phase + " " + st.Detail + "\n")
	}
	for _, want := range []string{"/api/users?id=7", `upstream "app"`, "2 backend(s)", "least_conn", "sets the request header X-Env", "removes the request header X-Drop"} {
		if !strings.Contains(all.String(), want) {
			t.Errorf("steps lack %q:\n%s", want, all.String())
		}
	}
	if rep := Explain(cfg, Request{Host: "a.test", Path: "/gone/x"}, nil); rep.Outcome.Kind != KindError {
		t.Errorf("an unknown upstream: %+v", rep.Outcome)
	}
}

func TestExplainMentionsCompression(t *testing.T) {
	s := site("a.test", nil, "web")
	s.Root = "/srv/a"
	s.Routes = []model.Route{{Kind: model.RoutePrefix, Path: "/", Actions: ret(200, "ok")}}
	cfg := cfgWith(s)
	steps := func() string {
		var b strings.Builder
		for _, st := range Explain(cfg, Request{Host: "a.test", Path: "/"}, nil).Steps {
			b.WriteString(st.Phase + ": " + st.Detail + "\n")
		}
		return b.String()
	}
	if strings.Contains(steps(), "compression") {
		t.Errorf("a site without compression mentions it:\n%s", steps())
	}
	c := model.DefaultCompression()
	cfg.Sites[0].Compression = &c
	out := steps()
	for _, want := range []string{"compression: ", "br, zstd, gzip", "at least 1024 bytes"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "precompressed") {
		t.Errorf("precompressed copies mentioned while off:\n%s", out)
	}
	c.Precompressed = true
	if !strings.Contains(steps(), "precompressed copies") {
		t.Errorf("precompressed not mentioned:\n%s", steps())
	}
}

func TestExplainDescribesTheCacheOfARoute(t *testing.T) {
	s := site("a.test", nil, "web")
	s.Root = "/srv/a"
	s.Routes = []model.Route{
		{Kind: model.RoutePrefix, Path: "/api/", Actions: model.Actions{Proxy: "app"},
			Cache: &model.RouteCache{Zone: "pages", TTL: model.Duration(5 * time.Minute), Cookies: model.CookiesBypassAny}},
		{Kind: model.RoutePrefix, Path: "/blog/", Actions: model.Actions{Proxy: "app"},
			Cache: &model.RouteCache{Zone: "pages", Cookies: model.CookiesBypassNamed, BypassCookies: []string{"wordpress_logged_in_*", "PHPSESSID"}}},
		{Kind: model.RoutePrefix, Path: "/open/", Actions: model.Actions{Proxy: "app"},
			Cache: &model.RouteCache{Zone: "pages", Cookies: model.CookiesBypassNone}},
		{Kind: model.RoutePrefix, Path: "/plain/", Actions: model.Actions{Proxy: "app"}},
	}
	cfg := cfgWith(s)
	cfg.Upstreams = []model.Upstream{{Name: "app", Backends: []model.UpstreamBackend{{Address: "http://h:1"}}}}
	steps := func(path string) string {
		rep := Explain(cfg, Request{Host: "a.test", Path: path}, nil)
		var b strings.Builder
		for _, st := range rep.Steps {
			b.WriteString(st.Phase + ": " + st.Detail + "\n")
		}
		return b.String()
	}
	for path, wants := range map[string][]string{
		"/api/x":  {`cache: cache zone "pages"`, "an Authorization header or any cookie", "ttl 5m0s"},
		"/blog/x": {"one of the cookies wordpress_logged_in_*, PHPSESSID"},
		"/open/x": {"requests with an Authorization header never use the cache"},
	} {
		out := steps(path)
		for _, w := range wants {
			if !strings.Contains(out, w) {
				t.Errorf("%s: lacks %q:\n%s", path, w, out)
			}
		}
	}
	if strings.Contains(steps("/plain/x"), "cache") {
		t.Errorf("a route without a cache mentions one:\n%s", steps("/plain/x"))
	}
}
