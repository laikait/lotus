package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/laikait/lotus/internal/config/diag"
	"github.com/laikait/lotus/internal/config/model"
)

const compressionServer = `lotus_config_version 1
server {
    listen "http" { address "127.0.0.1:8080" }
%s
    site "a.example" {
        root "/srv/a"
%s
    }
    site "b.example" {
        root "/srv/b"
    }
}`

func compressionConf(server, site string) string {
	return portable(strings.Replace(strings.Replace(compressionServer, "%s", server, 1), "%s", site, 1))
}

func TestCompressionIsOffUnlessConfigured(t *testing.T) {
	c := compile(t, compressionConf("", ""))
	if c.Compression != nil || c.Sites[0].Compression != nil || c.Sites[1].Compression != nil {
		t.Errorf("%+v %+v", c.Compression, c.Sites[0].Compression)
	}
}

func TestCompressionDefaults(t *testing.T) {
	c := compile(t, compressionConf("compression", ""))
	want := model.DefaultCompression()
	got := c.Sites[0].Compression
	if got == nil || strings.Join(got.Algorithms, ",") != "br,zstd,gzip" || got.MinSize != 1024 || got.Level != "default" || got.Precompressed ||
		strings.Join(got.Types, ",") != strings.Join(want.Types, ",") {
		t.Fatalf("%+v", got)
	}
	if c.Sites[1].Compression == nil || c.Compression == nil {
		t.Error("the server's block must reach every site")
	}
	// The block can also be empty braces, or say enabled true.
	if c := compile(t, compressionConf("compression { enabled true }", "")); c.Sites[0].Compression == nil {
		t.Error("enabled true")
	}
}

func TestCompressionSettingsAndInheritance(t *testing.T) {
	c := compile(t, compressionConf(`compression {
        algorithms [ "gzip", "br" ]
        types [ "text/*", "application/json" ]
        min_size 2kib
        level fast
        precompressed true
    }`, `compression {
        min_size 512b
        level best
    }`))
	srv, a, b := c.Compression, c.Sites[0].Compression, c.Sites[1].Compression
	if srv == nil || strings.Join(srv.Algorithms, ",") != "gzip,br" || srv.MinSize != 2048 || srv.Level != "fast" || !srv.Precompressed ||
		strings.Join(srv.Types, ",") != "text/*,application/json" {
		t.Errorf("server: %+v", srv)
	}
	// The site overrides setting by setting.
	if a == nil || strings.Join(a.Algorithms, ",") != "gzip,br" || a.MinSize != 512 || a.Level != "best" || !a.Precompressed ||
		strings.Join(a.Types, ",") != "text/*,application/json" {
		t.Errorf("site a: %+v", a)
	}
	if b == nil || b.MinSize != 2048 || b.Level != "fast" {
		t.Errorf("site b inherits the server's: %+v", b)
	}
	// The site's block didn't change the server's.
	if srv.MinSize != 2048 || srv.Level != "fast" {
		t.Errorf("the server's settings were changed by a site: %+v", srv)
	}
}

func TestSiteCanSwitchCompressionOffOrOn(t *testing.T) {
	off := compile(t, compressionConf("compression", "compression { enabled false }"))
	if off.Sites[0].Compression != nil || off.Sites[1].Compression == nil {
		t.Errorf("a site opting out: %+v %+v", off.Sites[0].Compression, off.Sites[1].Compression)
	}
	on := compile(t, compressionConf("", `compression { algorithms [ "gzip" ] }`))
	if on.Sites[0].Compression == nil || strings.Join(on.Sites[0].Compression.Algorithms, ",") != "gzip" || on.Sites[1].Compression != nil {
		t.Errorf("a site opting in: %+v %+v", on.Sites[0].Compression, on.Sites[1].Compression)
	}
	disabled := compile(t, compressionConf("compression { enabled false }", ""))
	if disabled.Compression != nil || disabled.Sites[0].Compression != nil {
		t.Error("a disabled server block must turn it off")
	}
}

func TestCompressionAppearsInTheInspectedConfiguration(t *testing.T) {
	c := compile(t, compressionConf(`compression { algorithms [ "br" ] }`, ""))
	b, _ := json.Marshal(c)
	if !strings.Contains(string(b), `"compression":{"algorithms":["br"]`) {
		t.Errorf("%s", b)
	}
}

func TestCompressionErrors(t *testing.T) {
	for name, tc := range map[string]struct{ block, code, text string }{
		"type with capitals":   {`compression { types [ "Text/HTML" ] }`, diag.CodeInvalidValue, "not a media type"},
		"type without a slash": {`compression { types [ "html" ] }`, diag.CodeInvalidValue, "not a media type"},
		"everything":           {`compression { types [ "*/*" ] }`, diag.CodeInvalidValue, "not a media type"},
		"empty subtype":        {`compression { types [ "text/" ] }`, diag.CodeInvalidValue, "not a media type"},
		"empty type list":      {`compression { types [] }`, "", "types"},
		"unknown algorithm":    {`compression { algorithms [ "deflate" ] }`, "", "algorithms"},
		"repeated algorithm":   {`compression { algorithms [ "gzip", "gzip" ] }`, diag.CodeInvalidValue, "listed twice"},
		"no algorithm":         {`compression { algorithms [] }`, "", "algorithms"},
		"min size too big":     {`compression { min_size 2mib }`, diag.CodeInvalidValue, "1mib"},
		"bad level":            {`compression { level ultra }`, "", "level"},
		"not a bool":           {`compression { precompressed "yes" }`, "", "precompressed"},
		"unknown directive":    {`compression { brotli_quality 11 }`, "", "brotli_quality"},
		"twice":                {"compression\ncompression", "", "compression"},
	} {
		_, diags := Check("main.lotus", optsFor(map[string]string{"main.lotus": compressionConf("    "+tc.block, "")}))
		out := format(diags)
		if len(errorsOf(diags)) == 0 || (tc.code != "" && !strings.Contains(out, tc.code)) || !strings.Contains(out, tc.text) {
			t.Errorf("%s: want %s %q in:\n%s", name, tc.code, tc.text, out)
		}
	}
	// The same checks apply in a site.
	_, diags := Check("main.lotus", optsFor(map[string]string{"main.lotus": compressionConf("", `compression { types [ "Bad" ] }`)}))
	if len(errorsOf(diags)) == 0 {
		t.Error("a bad type in a site was accepted")
	}
	// A min_size of exactly 1mib is fine.
	compile(t, compressionConf("compression { min_size 1mib }", ""))
}

func TestCompressionIsAdminOnly(t *testing.T) {
	files := map[string]string{
		"main.lotus":   compressionConf("", `include "tenant.lotus"`),
		"tenant.lotus": "compression { level best }",
	}
	_, diags := Check("main.lotus", optsFor(files))
	if len(errorsOf(diags)) == 0 {
		t.Errorf("a tenant file turned compression on:\n%s", format(diags))
	}
}
