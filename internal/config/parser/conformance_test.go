package parser

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/laikait/lotus/internal/config/ast"
	"github.com/laikait/lotus/internal/config/diag"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixtureDir = "../../../tests/fixtures/config"

// Valid fixtures must parse without diagnostics and match their .golden dump.
func TestValidFixtures(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(fixtureDir, "valid", "*.lotus"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no valid fixtures found (err=%v)", err)
	}
	for _, path := range files {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			f, diags := Parse(name, src)
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics:\n%s", formatDiags(diags))
			}
			got := ast.Dump(f)
			golden := strings.TrimSuffix(path, ".lotus") + ".golden"
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("missing golden file (run: go test ./internal/config/parser -update): %v", err)
			}
			// Golden files are compared with LF endings so Git autocrlf can't break them.
			if w := strings.ReplaceAll(string(want), "\r\n", "\n"); got != w {
				t.Errorf("AST dump differs from %s\n--- got ---\n%s\n--- want ---\n%s", golden, got, w)
			}
		})
	}
}

var expectRe = regexp.MustCompile(`^# expect: (LOTUS-CFG-\d{4}) (\d+):(\d+)$`)

type expectation struct {
	code      string
	line, col int
}

// Invalid fixtures start with `# expect: CODE line:col` lines. The errors the
// parser reports must match them exactly, in order.
func TestInvalidFixtures(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(fixtureDir, "invalid", "*.lotus"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no invalid fixtures found (err=%v)", err)
	}
	for _, path := range files {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var want []expectation
			sc := bufio.NewScanner(bytes.NewReader(src))
			for sc.Scan() {
				line := sc.Text()
				if !strings.HasPrefix(line, "# expect:") {
					break
				}
				m := expectRe.FindStringSubmatch(line)
				if m == nil {
					t.Fatalf("malformed expect line %q", line)
				}
				var e expectation
				e.code = m[1]
				fmt.Sscanf(m[2], "%d", &e.line)
				fmt.Sscanf(m[3], "%d", &e.col)
				want = append(want, e)
			}
			if len(want) == 0 {
				t.Fatal("fixture has no `# expect:` header")
			}

			_, diags := Parse(name, src)
			var got []expectation
			for _, d := range diags {
				if d.Severity == diag.Error {
					got = append(got, expectation{d.Code, d.Pos.Line, d.Pos.Col})
				}
			}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("errors differ\n got: %v\nwant: %v\nall diagnostics:\n%s", got, want, formatDiags(diags))
			}
		})
	}
}

func formatDiags(diags []diag.Diagnostic) string {
	var b strings.Builder
	for _, d := range diags {
		b.WriteString(d.Error())
		b.WriteByte('\n')
	}
	return b.String()
}
