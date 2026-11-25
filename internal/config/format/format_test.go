package format

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/laikait/lotus/internal/config/ast"
	"github.com/laikait/lotus/internal/config/parser"
)

// shape is the tree without positions, spacing or line layout: what the file
// means, plus the comments it carries.
func shape(f *ast.File) string {
	var b strings.Builder
	var val func(v ast.Value) string
	val = func(v ast.Value) string {
		switch x := v.(type) {
		case *ast.String:
			return "s" + x.Source
		case *ast.Int:
			return "i" + x.Source
		case *ast.Size:
			return "z" + x.Source
		case *ast.Duration:
			return "d" + x.Source
		case *ast.Bool:
			if x.Value {
				return "true"
			}
			return "false"
		case *ast.Null:
			return "null"
		case *ast.Ident:
			return "id:" + x.Name
		case *ast.Variable:
			return "$" + x.Name
		case *ast.Operator:
			return "op:" + x.Op
		case *ast.Call:
			parts := []string{}
			for _, a := range x.Args {
				parts = append(parts, val(a))
			}
			return x.Name + "(" + strings.Join(parts, ",") + ")"
		case *ast.List:
			parts := []string{}
			for _, a := range x.Items {
				parts = append(parts, val(a))
			}
			return "[" + strings.Join(parts, ",") + "]"
		}
		return "?"
	}
	cm := func(cs []ast.Comment) string {
		var out []string
		for _, c := range cs {
			out = append(out, strings.TrimRight(c.Text, " \t\r"))
		}
		return strings.Join(out, "|")
	}
	var stmts func(list []ast.Statement, depth int)
	stmts = func(list []ast.Statement, depth int) {
		pad := strings.Repeat(" ", depth)
		for _, s := range list {
			m := s.Meta()
			t := ""
			if m.Trailing != nil {
				t = strings.TrimRight(m.Trailing.Text, " \t\r")
			}
			switch x := s.(type) {
			case *ast.Directive:
				args := []string{}
				for _, a := range x.Args {
					args = append(args, val(a))
				}
				b.WriteString(pad + "D " + x.Name + " " + strings.Join(args, " ") + " ;" + t + " lead:" + cm(m.Leading) + "\n")
				if x.Block != nil {
					stmts(x.Block.Statements, depth+1)
					b.WriteString(pad + " dangling:" + cm(x.Block.Dangling) + "\n")
				}
			case *ast.Let:
				b.WriteString(pad + "L " + x.Name + " " + val(x.Value) + " ;" + t + " lead:" + cm(m.Leading) + "\n")
			case *ast.Include:
				b.WriteString(pad + "I " + x.Path.Source + " ;" + t + " lead:" + cm(m.Leading) + "\n")
			case *ast.Pair:
				b.WriteString(pad + "P " + x.Key.Source + " " + val(x.Value) + " ;" + t + " lead:" + cm(m.Leading) + "\n")
			}
		}
	}
	stmts(f.Statements, 0)
	b.WriteString("dangling:" + cm(f.Dangling) + "\n")
	return b.String()
}

func mustFormat(t *testing.T, src string) string {
	t.Helper()
	out, _, err := Source("t.lotus", []byte(src))
	if err != nil {
		t.Fatalf("%v\n%s", err, src)
	}
	return string(out)
}

func TestLayout(t *testing.T) {
	got := mustFormat(t, "lotus_config_version 1\nserver{\nlisten   \"http\" {address \"0.0.0.0:80\";protocol http}\n\n\n\nsite \"a.test\" { root \"/srv\" ; index [\"a.html\",\"b.html\"]\n}\n}\n")
	want := `lotus_config_version 1
server {
    listen "http" {
        address "0.0.0.0:80"
        protocol http
    }

    site "a.test" {
        root "/srv"
        index [ "a.html", "b.html" ]
    }
}
`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestCommentsAreKept(t *testing.T) {
	src := `# header

# about the server
server { # after the brace
    # first
    listen "http" { address "0.0.0.0:80" } # trailing on a block

    # about the site
    site "a.test" {
        root "/srv"   # the root


        # lonely, before the end
    }
    # last thing in the server
}
# end of file
`
	got := mustFormat(t, src)
	want := `# header

# about the server
server {
    # after the brace
    # first
    listen "http" {
        address "0.0.0.0:80"
    } # trailing on a block

    # about the site
    site "a.test" {
        root "/srv" # the root

        # lonely, before the end
    }
    # last thing in the server
}
# end of file
`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestLongListsAreSplit(t *testing.T) {
	long := `trusted_proxies [ "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16", "fc00::/7" ]`
	got := mustFormat(t, "server {\n"+long+"\n}\n")
	want := `server {
    trusted_proxies [
        "10.0.0.0/8",
        "172.16.0.0/12",
        "192.168.0.0/16",
        "100.64.0.0/10",
        "169.254.0.0/16",
        "fc00::/7",
    ]
}
`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	// Exactly at the limit stays on one line; one over is split.
	exact := "x [ " + strings.Repeat("a", MaxLine-len("    x [ ")-len(" ]")) + " ]"
	if len("    "+exact) != MaxLine {
		t.Fatal(len("    " + exact))
	}
	if g := mustFormat(t, "server {\n"+exact+"\n}\n"); strings.Contains(g, ",\n") {
		t.Errorf("100 columns fit:\n%s", g)
	}
	over := "x [ " + strings.Repeat("a", MaxLine-len("    x [ ")-len(" ]")+1) + " ]"
	if g := mustFormat(t, "server {\n"+over+"\n}\n"); !strings.Contains(g, "[\n") {
		t.Errorf("101 columns split:\n%s", g)
	}
	// let and map entries too.
	g := mustFormat(t, "let $nets = "+long[len("trusted_proxies "):]+"\n")
	if !strings.HasPrefix(g, "let $nets = [\n    \"10.0.0.0/8\",") || !strings.HasSuffix(g, "\n]\n") {
		t.Errorf("let list:\n%s", g)
	}
}

func TestValuesKeepTheirSpelling(t *testing.T) {
	src := "server {\n    a 10_000 100mb 1h30m 'raw \\d' \"q${x}\" true false null auto $v env(\"X\") [ ] ~* 'p'\n}\n"
	got := mustFormat(t, src)
	if !strings.Contains(got, `a 10_000 100mb 1h30m 'raw \d' "q${x}" true false null auto $v env("X") [] ~* 'p'`) {
		t.Errorf("%s", got)
	}
}

func TestSemicolonsAndEmptyBlocks(t *testing.T) {
	got := mustFormat(t, "a 1; b 2;c {}\n")
	if got != "a 1\nb 2\nc {\n}\n" {
		t.Errorf("%q", got)
	}
	if got := mustFormat(t, ""); got != "" {
		t.Errorf("empty file: %q", got)
	}
	if got := mustFormat(t, "\n\n# only a comment\n\n\n"); got != "# only a comment\n" {
		t.Errorf("%q", got)
	}
}

func TestSyntaxErrorsAreRefused(t *testing.T) {
	_, diags, err := Source("t.lotus", []byte("server {\n  listen\n"))
	if err != ErrSyntax || len(diags) == 0 {
		t.Errorf("%v %v", err, diags)
	}
}

// Formatting must not change what a file means, and doing it twice changes
// nothing. Files with syntax errors are skipped (they are not formatted).
func checkStable(t *testing.T, name, src string) {
	t.Helper()
	out, _, err := Source(name, []byte(src))
	if err != nil {
		return
	}
	f1, _ := parser.Parse(name, []byte(src))
	f2, diags2 := parser.Parse(name, out)
	if len(diags2) > 0 {
		t.Fatalf("%s: the formatted file does not parse: %v\n%s", name, diags2, out)
	}
	if shape(f1) != shape(f2) {
		t.Fatalf("%s: formatting changed the file\n--- before ---\n%s\n--- after ---\n%s\n--- text ---\n%s", name, shape(f1), shape(f2), out)
	}
	again, _, err := Source(name, out)
	if err != nil || string(again) != string(out) {
		t.Fatalf("%s: formatting twice differs:\n%s\n----\n%s", name, out, again)
	}
}

func TestFixturesAndExamplesAreStable(t *testing.T) {
	var files []string
	for _, pat := range []string{"../../../tests/fixtures/config/valid/*.lotus", "../../../tests/fixtures/config/semantic/*.lotus", "../../../configs/examples/*.lotus", "../../../configs/examples/*/*.lotus"} {
		m, _ := filepath.Glob(pat)
		files = append(files, m...)
	}
	if len(files) < 15 {
		t.Fatalf("only %d files found", len(files))
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		checkStable(t, f, string(data))
	}
}

func FuzzFormat(f *testing.F) {
	for _, s := range []string{"a 1\n", "server {\n # c\n listen \"x\" { }\n}\n", "let $a = [1,2]\n", "\"k\" = v\n", "a [\n 1, # one\n 2\n] # end\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		checkStable(t, "fuzz.lotus", src)
	})
}

func TestShortListsInLetAndMapsStayOnOneLine(t *testing.T) {
	got := mustFormat(t, "let $a = [1, 2]\nsecurity {\n    \"X\" = [ \"a\" ]\n}\n")
	if got != "let $a = [ 1, 2 ]\nsecurity {\n    \"X\" = [ \"a\" ]\n}\n" {
		t.Errorf("%q", got)
	}
}
