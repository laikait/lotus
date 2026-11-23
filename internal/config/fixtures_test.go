package config

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/laikait/lotus/internal/config/diag"
	"github.com/laikait/lotus/internal/config/loader"
)

const semanticDir = "../../tests/fixtures/config/semantic"

// The permission checks on included files depend on how the repository was
// checked out, so fixture tests skip them (the checks have their own tests).
func fixtureOptions() loader.Options {
	return loader.Options{Getenv: testEnv, InsecureIncludes: true}
}

func TestSemanticValidFixtures(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(semanticDir, "valid", "*.lotus"))
	if len(files) == 0 {
		t.Fatal("no valid semantic fixtures found")
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			res, diags := Check(path, fixtureOptions())
			if res == nil || len(diags) != 0 {
				t.Fatalf("expected a clean result:\n%s", format(diags))
			}
		})
	}
}

func TestSemanticValidFixtureWithIncludesReadsAllFiles(t *testing.T) {
	res, diags := Check(filepath.Join(semanticDir, "valid", "with-includes.lotus"), fixtureOptions())
	if res == nil {
		t.Fatalf("diags:\n%s", format(diags))
	}
	if len(res.Files) != 3 {
		t.Errorf("files read = %v, want root + shop.lotus + shop-routes.inc", res.Files)
	}
}

var expectRe = regexp.MustCompile(`^# expect: (LOTUS-CFG-\d{4}) (\d+):(\d+)$`)

// Invalid fixtures begin with `# expect: CODE line:col` lines. The errors must
// match exactly and in order. (The file of an error is not checked.)
func TestSemanticInvalidFixtures(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(semanticDir, "invalid", "*.lotus"))
	if len(files) == 0 {
		t.Fatal("no invalid semantic fixtures found")
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			sc := bufio.NewScanner(bytes.NewReader(src))
			for sc.Scan() && strings.HasPrefix(sc.Text(), "# expect:") {
				m := expectRe.FindStringSubmatch(sc.Text())
				if m == nil {
					t.Fatalf("malformed expect line %q", sc.Text())
				}
				want = append(want, fmt.Sprintf("%s %s:%s", m[1], m[2], m[3]))
			}
			if len(want) == 0 {
				t.Fatal("fixture has no `# expect:` header")
			}

			res, diags := Check(path, fixtureOptions())
			if res != nil {
				t.Fatal("an invalid fixture must not produce a result")
			}
			var got []string
			for _, d := range diags {
				if d.Severity == diag.Error {
					got = append(got, fmt.Sprintf("%s %d:%d", d.Code, d.Pos.Line, d.Pos.Col))
				}
			}
			if strings.Join(got, "; ") != strings.Join(want, "; ") {
				t.Errorf("errors differ\n got: %v\nwant: %v\nall diagnostics:\n%s", got, want, format(diags))
			}
		})
	}
}
