package docgen

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/laikait/lotus/internal/config"
	"github.com/laikait/lotus/internal/config/diag"
	"github.com/laikait/lotus/internal/config/loader"
	"github.com/laikait/lotus/internal/config/schema"
)

// A context's block, and where it sits.
type wrapper struct {
	parent schema.Context
	open   string // the line that opens the block
	// defaults are lines the block needs, written unless the example has them.
	defaults map[string]string
	extra    []string // lines always added (what the block needs around it)
	inside   []string // lines written when this block only holds a deeper block
}

var wrappers = map[schema.Context]wrapper{
	schema.CtxServer:          {},
	schema.CtxRuntime:         {parent: schema.CtxServer, open: "runtime {"},
	schema.CtxListen:          {parent: schema.CtxServer, open: `listen "extra" {`, defaults: map[string]string{"address": `address "127.0.0.1:8091"`}},
	schema.CtxLimits:          {parent: schema.CtxServer, open: "limits {"},
	schema.CtxTLS:             {parent: schema.CtxServer, open: "tls {"},
	schema.CtxACME:            {parent: schema.CtxTLS, open: "acme {"},
	schema.CtxDNS:             {parent: schema.CtxACME, open: "dns {"},
	schema.CtxMetrics:         {parent: schema.CtxServer, open: "metrics {", defaults: map[string]string{"address": `address "127.0.0.1:9100"`}},
	schema.CtxAdmin:           {parent: schema.CtxServer, open: "admin {", defaults: map[string]string{"address": `address "127.0.0.1:7080"`}},
	schema.CtxControl:         {parent: schema.CtxServer, open: "control {"},
	schema.CtxPHPBackend:      {parent: schema.CtxServer, open: `php_backend "extra" {`},
	schema.CtxUpstream:        {parent: schema.CtxServer, open: `upstream "extra" {`, defaults: map[string]string{"backend": `backend "http://127.0.0.1:9100"`}, inside: []string{`backend "http://127.0.0.1:9100"`}},
	schema.CtxBackend:         {parent: schema.CtxUpstream, open: `backend "http://127.0.0.1:9101" {`},
	schema.CtxHealthCheck:     {parent: schema.CtxUpstream, open: "health_check {"},
	schema.CtxCache:           {parent: schema.CtxServer, open: `cache "extra" {`},
	schema.CtxCompression:     {parent: schema.CtxServer, open: "compression {"},
	schema.CtxSite:            {parent: schema.CtxServer, open: `site "x.test" {`},
	schema.CtxLogs:            {parent: schema.CtxSite, open: "logs {"},
	schema.CtxRateLimit:       {parent: schema.CtxSite, open: "rate_limit {", defaults: map[string]string{"requests": "requests 10"}},
	schema.CtxSecurityHeaders: {parent: schema.CtxSite, open: "security_headers {"},
	schema.CtxRoute:           {parent: schema.CtxSite, open: `route "/x/" {`},
	schema.CtxMatch:           {parent: schema.CtxRoute, open: "match {"},
	schema.CtxRouteCache:      {parent: schema.CtxRoute, open: `cache "pages" {`},
}

// fixtures are what every example may refer to.
const fixtures = `    listen "http" {
        address "127.0.0.1:8080"
    }
    upstream "app" {
        backend "http://127.0.0.1:9000"
    }
    php_backend "php" {
        fastcgi_pass "127.0.0.1:9001"
    }
    cache "pages"
`

func firstWord(line string) string {
	f := strings.Fields(line)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// wrap puts an example inside the blocks that lead to its context.
func wrap(ctx schema.Context, example string) string {
	var chain []schema.Context
	for c := ctx; ; c = wrappers[c].parent {
		chain = append([]schema.Context{c}, chain...)
		if c == schema.CtxServer {
			break
		}
	}
	// The innermost block holds the example plus the lines it needs.
	body := strings.Split(strings.TrimSpace(example), "\n")
	have := map[string]bool{}
	for _, l := range body {
		have[firstWord(l)] = true
	}
	w := wrappers[ctx]
	var names []string
	for n := range w.defaults {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if !have[n] {
			body = append([]string{w.defaults[n]}, body...)
		}
	}
	body = append(body, w.extra...)

	var b strings.Builder
	b.WriteString("lotus_config_version 1\n\nserver {\n" + fixtures)
	depth := 1
	// chain[0] is the server itself, already open.
	for _, c := range chain[1:] {
		b.WriteString(strings.Repeat("    ", depth) + wrappers[c].open + "\n")
		depth++
		if c == schema.CtxSite && !(ctx == schema.CtxSite && have["root"]) {
			b.WriteString(strings.Repeat("    ", depth) + "root \"/srv/x\"\n")
		}
		if c != ctx {
			for _, l := range wrappers[c].inside {
				b.WriteString(strings.Repeat("    ", depth) + l + "\n")
			}
		}
		if c == schema.CtxRoute && ctx == schema.CtxRouteCache {
			b.WriteString(strings.Repeat("    ", depth) + "proxy \"app\"\n")
		}
	}
	for _, l := range body {
		b.WriteString(strings.Repeat("    ", depth) + l + "\n")
	}
	if ctx == schema.CtxServer {
		// The example is written as a directive of the server block.
		depth = 1
	}
	for depth > 1 {
		depth--
		b.WriteString(strings.Repeat("    ", depth) + "}\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// Every directive has an example, and every example is a valid configuration
// when put in its place.
func TestEveryExampleIsValid(t *testing.T) {
	reg := schema.Default()
	known := map[string]bool{}
	reg.All(func(ctx schema.Context, d *schema.Directive) {
		key := string(ctx) + "/" + d.Name
		known[key] = true
		ex := schema.ExampleFor(ctx, d.Name)
		if strings.TrimSpace(ex) == "" {
			t.Errorf("%s has no example", key)
			return
		}
		var text string
		if ctx == schema.CtxRoot {
			text = "lotus_config_version 1\n\n" + ex + "\n"
		} else {
			if _, ok := wrappers[ctx]; !ok {
				t.Errorf("%s: the test has no wrapper for the context %s", key, ctx)
				return
			}
			text = wrap(ctx, ex)
		}
		path := filepath.Join(t.TempDir(), "example.conf")
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		res, diags := config.Check(path, loader.Options{InsecureIncludes: true, Getenv: func(string) (string, bool) { return "x", true }})
		if res == nil {
			var msgs []string
			for _, dg := range diags {
				if dg.Severity == diag.Error {
					msgs = append(msgs, dg.Message+" (line "+strconv.Itoa(dg.Pos.Line)+")")
				}
			}
			t.Errorf("%s: the example is not valid:\n  %s\n--- file ---\n%s", key, strings.Join(msgs, "\n  "), text)
		}
	})
	for _, k := range schema.ExampleKeys() {
		if !known[k] {
			t.Errorf("there is an example for %s, which is not a directive", k)
		}
	}
}
