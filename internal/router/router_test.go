package router

import (
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"regexp"
	"strings"
	"testing"

	"github.com/laikait/lotus/internal/config/model"
)

func prefix(p string, kids ...model.Route) model.Route {
	return model.Route{Kind: model.RoutePrefix, Path: p, Children: kids}
}
func final(p string) model.Route { return model.Route{Kind: model.RouteFinalPrefix, Path: p} }
func exact(p string) model.Route { return model.Route{Kind: model.RouteExact, Path: p} }
func re(pattern string) model.Route {
	return model.Route{Kind: model.RouteRegex, Path: pattern, Regexp: regexp.MustCompile(pattern)}
}
func ref(pattern string) model.Route {
	return model.Route{Kind: model.RouteRegexFold, Path: pattern, Regexp: regexp.MustCompile("(?i)" + pattern)}
}

// label identifies a route in test output.
func label(f Found, ok bool) string {
	if !ok {
		return "none"
	}
	return string(f.Route.Kind) + " " + f.Route.Path
}

// The example in docs/architecture/routing-precedence.md §6, row by row.
func TestDocumentExamples(t *testing.T) {
	g := prefix("/docs/api/", re(`\.json$`))
	routes := []model.Route{
		exact("/"),           // A
		prefix("/"),          // B
		prefix("/docs/"),     // C
		final("/static/"),    // D
		ref(`\.(png|jpg)$`),  // E
		re(`^/docs/.*\.md$`), // F
		g,                    // G (with child G1)
	}
	cases := []struct{ path, want string }{
		{"/", "exact /"},
		{"/index.html", "prefix /"},
		{"/logo.png", `regex_fold \.(png|jpg)$`},
		{"/static/logo.png", "final_prefix /static/"},
		{"/docs/intro.md", `regex ^/docs/.*\.md$`},
		{"/docs/guide.html", "prefix /docs/"},
		{"/docs/api/spec.json", `regex \.json$`},
		{"/docs/api/readme.md", `regex ^/docs/.*\.md$`},
		{"/docs/api/x.png", `regex_fold \.(png|jpg)$`},
		{"/@fallback", "prefix /"},
		{"/docs/api/guide", "prefix /docs/api/"},
		{"/LOGO.PNG", `regex_fold \.(png|jpg)$`}, // ~* ignores case
		{"/docs/INTRO.MD", "prefix /docs/"},      // ~ does not
	}
	for _, c := range cases {
		if got := label(Find(routes, c.path)); got != c.want {
			t.Errorf("Find(%q) = %s, want %s", c.path, got, c.want)
		}
	}
}

func TestEdgeCases(t *testing.T) {
	if _, ok := Find(nil, "/x"); ok {
		t.Error("no routes should find nothing")
	}
	if got := label(Find([]model.Route{prefix("/")}, "/")); got != "prefix /" {
		t.Errorf("lone / = %s", got)
	}
	// Unicode paths compare byte by byte.
	if got := label(Find([]model.Route{prefix("/ü/")}, "/ü/x")); got != "prefix /ü/" {
		t.Errorf("unicode prefix = %s", got)
	}
	// A final prefix at the outer level stops outer regexes even when a nested
	// plain prefix refines the choice.
	routes := []model.Route{
		{Kind: model.RouteFinalPrefix, Path: "/a/", Children: []model.Route{prefix("/a/b/")}},
		re(`\.png$`),
	}
	if got := label(Find(routes, "/a/b/x.png")); got != "prefix /a/b/" {
		t.Errorf("nested prefix inside final = %s, want prefix /a/b/", got)
	}
	// A final prefix nested in a prefix wins over outer regexes.
	routes = []model.Route{prefix("/a/", final("/a/b/")), re(`\.png$`)}
	if got := label(Find(routes, "/a/b/x.png")); got != "final_prefix /a/b/" {
		t.Errorf("final nested = %s", got)
	}
	// Overlapping regexes: the first in configuration order wins.
	routes = []model.Route{re(`a`), re(`ab`)}
	if got := label(Find(routes, "/ab")); got != "regex a" {
		t.Errorf("overlapping regex = %s", got)
	}
	// Captures come from the chosen regex.
	f, ok := Find([]model.Route{re(`^/u/(\d+)$`)}, "/u/42")
	if !ok || len(f.Captures) != 2 || f.Captures[1] != "42" {
		t.Errorf("captures = %v", f.Captures)
	}
}

func TestNamedRoutesAreNeverMatchedByPaths(t *testing.T) {
	routes := []model.Route{prefix("/")}
	named := []model.Route{{Kind: model.RouteNamed, Path: "@fallback"}}
	if got := label(Find(routes, "/@fallback")); got != "prefix /" {
		t.Errorf("got %s", got)
	}
	if FindNamed(named, "@fallback") == nil || FindNamed(named, "@other") != nil {
		t.Error("FindNamed")
	}
}

func TestNormalizePath(t *testing.T) {
	cases := map[string]string{
		"":             "/",
		"/":            "/",
		"a":            "/a",
		"/a//b":        "/a/b",
		"/a/./b":       "/a/b",
		"/a/../b":      "/b",
		"/../../etc":   "/etc",
		"/a/":          "/a/",
		"/a/b/..":      "/a",
		"//evil.com/x": "/evil.com/x",
		"/a/b/../":     "/a/",
	}
	for in, want := range cases {
		if got := NormalizePath(in); got != want {
			t.Errorf("NormalizePath(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---- property test against a reference written directly from the document ----

// refFind is routing-precedence.md §3, transliterated step by step with no
// shared code. It reports the chosen route's identity.
func refFind(level []model.Route, p string) (*model.Route, bool) {
	// step 1
	for i := range level {
		if level[i].Kind == model.RouteExact && level[i].Path == p {
			return &level[i], true
		}
	}
	// step 2
	var P *model.Route
	for i := range level {
		k := level[i].Kind
		if (k == model.RoutePrefix || k == model.RouteFinalPrefix) && strings.HasPrefix(p, level[i].Path) {
			if P == nil || len(level[i].Path) > len(P.Path) {
				P = &level[i]
			}
		}
	}
	isFinal := P != nil && P.Kind == model.RouteFinalPrefix
	// step 3
	if P != nil && len(P.Children) > 0 {
		if r, ok := refFind(P.Children, p); ok {
			if r.Kind == model.RouteExact || r.Kind == model.RouteRegex ||
				r.Kind == model.RouteRegexFold || r.Kind == model.RouteFinalPrefix {
				return r, true
			}
			P = r
		}
	}
	// step 4
	if !isFinal {
		for i := range level {
			k := level[i].Kind
			if (k == model.RouteRegex || k == model.RouteRegexFold) && level[i].Regexp.MatchString(p) {
				return &level[i], true
			}
		}
	}
	return P, P != nil
}

func randomRoutes(rng *rand.Rand, base string, depth int) []model.Route {
	segs := []string{"a/", "b/", "c/"}
	var out []model.Route
	used := map[string]bool{}
	for n := rng.Intn(5); n > 0; n-- {
		switch rng.Intn(5) {
		case 0, 1, 2:
			p := base + segs[rng.Intn(len(segs))]
			if rng.Intn(2) == 0 {
				p += segs[rng.Intn(len(segs))]
			}
			key := "p" + p
			if used[key] {
				continue
			}
			used[key] = true
			r := model.Route{Kind: model.RoutePrefix, Path: p}
			if rng.Intn(4) == 0 {
				r.Kind = model.RouteFinalPrefix
			}
			if depth > 0 && rng.Intn(2) == 0 {
				r.Children = randomRoutes(rng, p, depth-1)
			}
			out = append(out, r)
		case 3:
			p := strings.TrimSuffix(base+segs[rng.Intn(len(segs))], "/")
			key := "e" + p
			if used[key] {
				continue
			}
			used[key] = true
			out = append(out, exact(p))
		case 4:
			pats := []string{`a`, `b/c`, `c$`, `^/a/`, `b$`}
			p := pats[rng.Intn(len(pats))]
			if rng.Intn(2) == 0 {
				out = append(out, re(p))
			} else {
				out = append(out, ref(p))
			}
		}
	}
	return out
}

func TestMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	paths := []string{"/", "/a", "/a/", "/a/b", "/a/b/", "/a/b/c", "/b/c/", "/b/a/c", "/c", "/c/c/c/", "/A/B"}
	for i := 0; i < 3000; i++ {
		routes := randomRoutes(rng, "/", 2)
		for _, p := range paths {
			got, gok := Find(routes, p)
			want, wok := refFind(routes, p)
			if gok != wok || (gok && got.Route != want) {
				t.Fatalf("routes %v, path %q: router chose %s, reference chose %v", describe(routes), p, label(got, gok), want)
			}
		}
	}
}

func describe(rs []model.Route) []string {
	var out []string
	for _, r := range rs {
		s := string(r.Kind) + ":" + r.Path
		if len(r.Children) > 0 {
			s += "{" + strings.Join(describe(r.Children), ",") + "}"
		}
		out = append(out, s)
	}
	return out
}

// ---- match blocks ----

func req(method, target string, hdr map[string]string) Request {
	r := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return Request{HTTP: r, ClientIP: netip.MustParseAddr("10.1.2.3"), Scheme: "https"}
}

func TestSelectMatch(t *testing.T) {
	post := model.Match{Methods: []string{"POST", "PUT"}, Actions: model.Actions{Return: &model.Return{Status: 405}}}
	notLAN := model.Match{ClientIP: &model.ClientIPCond{Not: true, Networks: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}},
		Actions: model.Actions{Return: &model.Return{Status: 403}}}
	inLAN := model.Match{ClientIP: &model.ClientIPCond{Networks: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}},
		Actions: model.Actions{Return: &model.Return{Status: 200}}}
	ua := model.Match{Headers: []model.Pattern{{Name: "User-Agent", Op: "~*", Value: "bot", Regexp: regexp.MustCompile("(?i)bot")}},
		Actions: model.Actions{Return: &model.Return{Status: 429}}}
	noBot := model.Match{Headers: []model.Pattern{{Name: "User-Agent", Op: "!~", Value: "bot", Regexp: regexp.MustCompile("bot")}},
		Actions: model.Actions{Return: &model.Return{Status: 204}}}
	q := model.Match{Query: []model.Pattern{{Name: "debug", Op: "=", Value: "1"}}, Actions: model.Actions{Return: &model.Return{Status: 418}}}
	ck := model.Match{Cookies: []model.Pattern{{Name: "s", Op: "=", Value: "x"}}, Actions: model.Actions{Return: &model.Return{Status: 401}}}
	hostM := model.Match{Hosts: []string{"*.example.com"}, Actions: model.Actions{Return: &model.Return{Status: 410}}}
	scheme := model.Match{Scheme: "http", Actions: model.Actions{Return: &model.Return{Status: 301}}}

	status := func(m *model.Match) int {
		if m == nil {
			return 0
		}
		return m.Actions.Return.Status
	}
	run := func(name string, m model.Match, r Request, want int) {
		t.Helper()
		rt := &model.Route{Matches: []model.Match{m}}
		if got := status(SelectMatch(rt, r)); got != want {
			t.Errorf("%s: got %d, want %d", name, got, want)
		}
	}
	run("method hit", post, req("POST", "/", nil), 405)
	run("method miss", post, req("GET", "/", nil), 0)
	run("client_ip not, outside", notLAN, withIP(req("GET", "/", nil), "8.8.8.8"), 403)
	run("client_ip not, inside", notLAN, req("GET", "/", nil), 0)
	run("client_ip in, inside", inLAN, req("GET", "/", nil), 200)
	run("client_ip in, outside", inLAN, withIP(req("GET", "/", nil), "8.8.8.8"), 0)
	run("client_ip unknown, in", inLAN, Request{HTTP: req("GET", "/", nil).HTTP}, 0)
	run("client_ip unknown, not", notLAN, Request{HTTP: req("GET", "/", nil).HTTP}, 403)
	run("ipv4-mapped", inLAN, withIP(req("GET", "/", nil), "::ffff:10.9.9.9"), 200)
	run("header regex fold", ua, req("GET", "/", map[string]string{"User-Agent": "GoogleBOT"}), 429)
	run("header missing, !~", noBot, req("GET", "/", nil), 204)
	run("header present, !~", noBot, req("GET", "/", map[string]string{"User-Agent": "bot"}), 0)
	run("query", q, req("GET", "/?debug=1", nil), 418)
	run("query miss", q, req("GET", "/?debug=2", nil), 0)
	r := req("GET", "/", nil)
	r.HTTP.AddCookie(&http.Cookie{Name: "s", Value: "x"})
	run("cookie", ck, r, 401)
	run("cookie miss", ck, req("GET", "/", nil), 0)
	h := req("GET", "/", nil)
	h.HTTP.Host = "www.Example.com:8080"
	run("host wildcard", hostM, h, 410)
	h.HTTP.Host = "example.com"
	run("host wildcard needs a subdomain", hostM, h, 0)
	hh := req("GET", "/", nil)
	hh.Scheme = "http"
	run("scheme", scheme, hh, 301)
	run("scheme miss", scheme, req("GET", "/", nil), 0)

	// All conditions of one block must hold (AND).
	both := model.Match{Methods: []string{"POST"}, Query: q.Query, Actions: q.Actions}
	run("AND, one fails", both, req("POST", "/?debug=2", nil), 0)
	run("AND, both hold", both, req("POST", "/?debug=1", nil), 418)

	// The first block that holds wins; later ones are ignored.
	rt := &model.Route{Matches: []model.Match{post, notLAN, inLAN}}
	if got := status(SelectMatch(rt, req("POST", "/", nil))); got != 405 {
		t.Errorf("first match = %d, want 405", got)
	}
	if got := status(SelectMatch(rt, withIP(req("GET", "/", nil), "8.8.8.8"))); got != 403 {
		t.Errorf("second match = %d, want 403", got)
	}
}

func withIP(r Request, ip string) Request {
	r.ClientIP = netip.MustParseAddr(ip)
	return r
}

func TestEffectiveReplacesByName(t *testing.T) {
	route := &model.Route{Actions: model.Actions{
		TryFiles: []string{"{uri}", "=404"}, PHP: "main", Return: &model.Return{Status: 200},
	}}
	if got := Effective(route, nil); got.PHP != "main" || got.Return.Status != 200 {
		t.Errorf("no match must keep route actions: %+v", got)
	}
	m := &model.Match{Actions: model.Actions{Return: &model.Return{Status: 403}}}
	got := Effective(route, m)
	if got.Return.Status != 403 || got.PHP != "main" || len(got.TryFiles) != 2 {
		t.Errorf("only the named action is replaced: %+v", got)
	}
	if route.Actions.Return.Status != 200 {
		t.Error("Effective must not modify the route")
	}
}

// ---- placeholders ----

func TestExpand(t *testing.T) {
	v := &Vars{
		URI: "/a%20b?x=1", Path: "/a b", Query: "x=1", Host: "example.com", Scheme: "https",
		Method: "GET", RemoteIP: "192.0.2.1", RequestID: "abc",
		Header:   func(n string) string { return map[string]string{"X-Token": "t0k"}[n] },
		QueryArg: func(n string) string { return map[string]string{"next": "/home"}[n] },
		Cookie:   func(n string) string { return map[string]string{"sid": "s1"}[n] },
		Captures: []string{"/u/42", "42"},
	}
	cases := map[string]string{
		"plain":                  "plain",
		"{uri}":                  "/a%20b?x=1",
		"/index.php?{query}":     "/index.php?x=1",
		"{scheme}://{host}{uri}": "https://example.com/a%20b?x=1",
		"{header.X-Token}":       "t0k",
		"{query.next}":           "/home",
		"{cookie.sid}":           "s1",
		"/user/{re.1}":           "/user/42",
		"{re.9}":                 "",
		"{method} {remote_ip}":   "GET 192.0.2.1",
		"{nonsense}":             "{nonsense}",
		"a{b":                    "a{b",
		"{{uri}}":                "{/a%20b?x=1}",
		"\x00{uri} literal":      "{uri} literal",
		"x\x00{y}{path}":         "x{y}/a b",
	}
	for in, want := range cases {
		got, err := Expand(in, v)
		if err != nil || got != want {
			t.Errorf("Expand(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestExpandRejectsControlCharacters(t *testing.T) {
	for _, val := range []string{"a\r\nSet-Cookie: x=1", "a\nb", "a\x00b"} {
		v := &Vars{Path: val}
		if _, err := Expand("/x{path}", v); err != ErrUnsafeValue {
			t.Errorf("value %q: err = %v, want ErrUnsafeValue", val, err)
		}
	}
	// Control characters written in the template itself are not placeholder values.
	if _, err := Expand("no placeholders", &Vars{}); err != nil {
		t.Error(err)
	}
}
