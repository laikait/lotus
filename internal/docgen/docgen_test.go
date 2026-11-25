package docgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/laikait/lotus/internal/config/schema"
)

// Every directive must say what it does and what can go wrong with it
// (docs/configuration/directive-template.md).
func TestEveryDirectiveIsDocumented(t *testing.T) {
	schema.Default().All(func(ctx schema.Context, d *schema.Directive) {
		where := string(ctx) + "/" + d.Name
		if strings.TrimSpace(d.Description) == "" {
			t.Errorf("%s has no description", where)
		}
		if strings.TrimSpace(d.Security) == "" {
			t.Errorf("%s has no security note (write \"None.\" only if that is true)", where)
		}
		if d.Since == "" {
			t.Errorf("%s has no Since", where)
		}
		if d.Default == "" {
			t.Errorf("%s has no default (write \"none\")", where)
		}
	})
}

// The pages on disk are what the registry produces. After changing a
// directive, run `make docs`.
func TestGeneratedPagesAreCurrent(t *testing.T) {
	dir := filepath.Join("..", "..", "docs", "configuration", "directives")
	pages := Generate(schema.Default())
	for name, want := range pages {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("%s is missing: run `make docs`", name)
			continue
		}
		if strings.ReplaceAll(string(got), "\r\n", "\n") != want {
			t.Errorf("%s is out of date: run `make docs`", name)
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.md"))
	for _, f := range files {
		if _, ok := pages[filepath.Base(f)]; !ok {
			t.Errorf("%s documents a directive that no longer exists: run `make docs`", filepath.Base(f))
		}
	}
}

func TestPagesHaveTheExpectedShape(t *testing.T) {
	pages := Generate(schema.Default())
	p := pages["max_request_body.md"]
	for _, want := range []string{"# `max_request_body`", "max_request_body <size>", "**Default**", "10mib", "### Description", "### Security"} {
		if !strings.Contains(p, want) {
			t.Errorf("max_request_body.md lacks %q:\n%s", want, p)
		}
	}
	// A directive defined differently in two places gets a section for each.
	if c := pages["cache.md"]; strings.Count(c, "## In ") < 2 {
		t.Errorf("cache is defined at the server and in a route:\n%s", c)
	}
	// Secrets and administrator-only settings are marked.
	if !strings.Contains(pages["bearer_token.md"], "redacted") {
		t.Error("bearer_token is a secret")
	}
	if !strings.Contains(pages["php_backend.md"], "administrators only") {
		t.Error("php_backend is administrator-only")
	}
	if !strings.Contains(pages["index.md"], "[`route`](route.md)") {
		t.Error("the index lists route")
	}
	// Output is deterministic.
	again := Generate(schema.Default())
	for n, p := range pages {
		if again[n] != p {
			t.Fatalf("%s differs between runs", n)
		}
	}
}
