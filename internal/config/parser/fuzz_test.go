package parser

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/laikait/lotus/internal/config/ast"
	"github.com/laikait/lotus/internal/config/diag"
)

// FuzzParse checks that arbitrary input never panics or hangs, that reported
// positions are sane, and that parsing is deterministic.
// Run it with: go test ./internal/config/parser -fuzz=FuzzParse -fuzztime=60s
func FuzzParse(f *testing.F) {
	for _, dir := range []string{"valid", "invalid"} {
		files, _ := filepath.Glob(filepath.Join(fixtureDir, dir, "*.lotus"))
		for _, path := range files {
			if src, err := os.ReadFile(path); err == nil {
				f.Add(src)
			}
		}
	}
	f.Add([]byte(`a "${x}" '\d' 1h30m 100mib [1,(2)] env("A")`))
	f.Add([]byte("a {\n\"k\" = 1\n}\r\n"))

	f.Fuzz(func(t *testing.T, src []byte) {
		file, diags := Parse("fuzz.lotus", src)
		for _, d := range diags {
			if d.Pos.Line < 1 || d.Pos.Col < 1 {
				t.Fatalf("diagnostic with invalid position: %+v", d)
			}
			if d.Pos.Offset < 0 || d.Pos.Offset > len(src) {
				t.Fatalf("diagnostic offset %d outside input of %d bytes: %+v", d.Pos.Offset, len(src), d)
			}
		}
		first := ast.Dump(file)

		file2, diags2 := Parse("fuzz.lotus", src)
		if len(diags) != len(diags2) || first != ast.Dump(file2) {
			t.Fatal("parsing is not deterministic")
		}

		errs := 0
		for _, d := range diags {
			if d.Severity == diag.Error {
				errs++
			}
		}
		if errs > diag.DefaultMaxErrors {
			t.Fatalf("%d errors reported; the limit is %d", errs, diag.DefaultMaxErrors)
		}
	})
}
