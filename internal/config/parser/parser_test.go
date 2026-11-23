package parser

import (
	"strings"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/config/ast"
	"github.com/laikait/lotus/internal/config/diag"
)

func mustParse(t *testing.T, src string) *ast.File {
	t.Helper()
	f, diags := Parse("t.lotus", []byte(src))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics:\n%s", formatDiags(diags))
	}
	return f
}

func directive(t *testing.T, s ast.Statement) *ast.Directive {
	t.Helper()
	d, ok := s.(*ast.Directive)
	if !ok {
		t.Fatalf("statement is %T, want *ast.Directive", s)
	}
	return d
}

func TestDirectiveStructure(t *testing.T) {
	f := mustParse(t, `site "example.com" {
    root "/var/www"
    route ~ '\.php$' {
        php "php83"
    }
}
`)
	if len(f.Statements) != 1 {
		t.Fatalf("got %d statements", len(f.Statements))
	}
	site := directive(t, f.Statements[0])
	if site.Name != "site" || len(site.Args) != 1 || site.Block == nil {
		t.Fatalf("bad site directive: %+v", site)
	}
	if got, ok := site.Args[0].(*ast.String).Plain(); !ok || got != "example.com" {
		t.Errorf("site name = %q", got)
	}
	route := directive(t, site.Block.Statements[1])
	if len(route.Args) != 2 {
		t.Fatalf("route args = %d, want 2", len(route.Args))
	}
	if op := route.Args[0].(*ast.Operator); op.Op != "~" {
		t.Errorf("operator = %q", op.Op)
	}
	re := route.Args[1].(*ast.String)
	if plain, _ := re.Plain(); !re.Raw || plain != `\.php$` {
		t.Errorf("regex = %q raw=%v", plain, re.Raw)
	}
	if site.Block.Open.Line != 1 || site.Block.Close.Line != 6 {
		t.Errorf("block span = %v..%v", site.Block.Open, site.Block.Close)
	}
}

func TestValueTypes(t *testing.T) {
	f := mustParse(t, `x 42 100mb 1h30m true null auto $v "s" [1, 2] env("A", "b")`)
	args := directive(t, f.Statements[0]).Args
	if len(args) != 10 {
		t.Fatalf("got %d args", len(args))
	}
	if v := args[0].(*ast.Int); v.Value != 42 || v.Source != "42" {
		t.Errorf("int = %+v", v)
	}
	if v := args[1].(*ast.Size); v.Bytes != 100_000_000 || v.Source != "100mb" {
		t.Errorf("size = %+v", v)
	}
	if v := args[2].(*ast.Duration); v.Value != 90*time.Minute || v.Source != "1h30m" {
		t.Errorf("duration = %+v", v)
	}
	if !args[3].(*ast.Bool).Value {
		t.Error("true parsed as false")
	}
	if _, ok := args[4].(*ast.Null); !ok {
		t.Errorf("null = %T", args[4])
	}
	if v := args[5].(*ast.Ident); v.Name != "auto" {
		t.Errorf("ident = %+v", v)
	}
	if v := args[6].(*ast.Variable); v.Name != "v" {
		t.Errorf("variable = %+v", v)
	}
	if v := args[8].(*ast.List); len(v.Items) != 2 {
		t.Errorf("list = %+v", v)
	}
	if v := args[9].(*ast.Call); v.Name != "env" || len(v.Args) != 2 {
		t.Errorf("call = %+v", v)
	}
}

func TestLetIncludePair(t *testing.T) {
	f := mustParse(t, `let $a = "x"
include "y/*.lotus"
m {
    "k" = 1
}
`)
	let := f.Statements[0].(*ast.Let)
	if let.Name != "a" {
		t.Errorf("let name = %q", let.Name)
	}
	inc := f.Statements[1].(*ast.Include)
	if p, _ := inc.Path.Plain(); p != "y/*.lotus" {
		t.Errorf("include path = %q", p)
	}
	pair := directive(t, f.Statements[2]).Block.Statements[0].(*ast.Pair)
	if k, _ := pair.Key.Plain(); k != "k" {
		t.Errorf("pair key = %q", k)
	}
}

func TestExactRouteIsNotLet(t *testing.T) {
	// `route = "/x"` uses '=' as an argument; only `let` assigns.
	f := mustParse(t, `route = "/x" { return 200 }`)
	d := directive(t, f.Statements[0])
	if op, ok := d.Args[0].(*ast.Operator); !ok || op.Op != "=" {
		t.Errorf("first arg = %#v", d.Args[0])
	}
}

func TestCommentsAndBlankLines(t *testing.T) {
	f := mustParse(t, `# top

a 1 # after a
# lead b1
# lead b2

b 2
c {
    # in c
    d 1
    # end of c
}
`)
	a := directive(t, f.Statements[0])
	if a.Trailing == nil || a.Trailing.Text != "# after a" {
		t.Errorf("a.Trailing = %+v", a.Trailing)
	}
	// "# top" followed by a blank line is a leading comment of a, with a blank after it.
	if len(a.Leading) != 1 || a.Leading[0].Text != "# top" {
		t.Errorf("a.Leading = %+v", a.Leading)
	}
	if !a.BlankBefore {
		t.Error("a: blank line between the comment and the directive was lost")
	}
	b := directive(t, f.Statements[1])
	if len(b.Leading) != 2 || b.Leading[0].Text != "# lead b1" || b.Leading[1].Text != "# lead b2" {
		t.Errorf("b.Leading = %+v", b.Leading)
	}
	if b.Leading[0].BlankBefore {
		t.Error("b1 comment follows a directly; no blank line before it")
	}
	if !b.BlankBefore {
		t.Error("b: blank line between its comments and b was lost")
	}
	c := directive(t, f.Statements[2])
	if c.BlankBefore {
		t.Error("c directly follows b")
	}
	d := directive(t, c.Block.Statements[0])
	if len(d.Leading) != 1 || d.Leading[0].Text != "# in c" {
		t.Errorf("d.Leading = %+v", d.Leading)
	}
	if len(c.Block.Dangling) != 1 || c.Block.Dangling[0].Text != "# end of c" {
		t.Errorf("c dangling = %+v", c.Block.Dangling)
	}
}

func TestCommentAfterBraceBelongsToFirstStatement(t *testing.T) {
	f := mustParse(t, "site \"x\" { # note\n    root \"/a\"\n}\n")
	site := directive(t, f.Statements[0])
	if site.Trailing != nil {
		t.Errorf("site.Trailing = %+v, want nil", site.Trailing)
	}
	root := directive(t, site.Block.Statements[0])
	if len(root.Leading) != 1 || root.Leading[0].Text != "# note" {
		t.Errorf("root.Leading = %+v", root.Leading)
	}
}

func TestSingleLineBlocksAndSemicolons(t *testing.T) {
	f := mustParse(t, "limits { a 1; b 2 }\nempty {}\nx 1; y 2\n")
	if len(f.Statements) != 4 {
		t.Fatalf("got %d statements, want 4", len(f.Statements))
	}
	if n := len(directive(t, f.Statements[0]).Block.Statements); n != 2 {
		t.Errorf("limits has %d statements", n)
	}
	if directive(t, f.Statements[1]).Block == nil {
		t.Error("empty block is nil")
	}
}

func TestErrorRecoveryReportsLaterErrors(t *testing.T) {
	_, diags := Parse("t.lotus", []byte("a \"\\q\"\nb 1x\nc ]\nd 1\ne @\n"))
	var codes []string
	for _, d := range diags {
		codes = append(codes, d.Code)
	}
	want := []string{diag.CodeInvalidEscape, diag.CodeInvalidNumber, diag.CodeUnexpectedToken, diag.CodeUnexpectedChar}
	if strings.Join(codes, ",") != strings.Join(want, ",") {
		t.Errorf("codes = %v, want %v\n%s", codes, want, formatDiags(diags))
	}
}

func TestBadBlockHeaderSkipsWholeBlock(t *testing.T) {
	// The error is in the header; the block body must be skipped as one unit
	// so its contents and closing brace don't cause follow-up errors.
	_, diags := Parse("t.lotus", []byte("site ] {\n    root \"/x\"\n    inner {\n        a 1\n    }\n}\nok 1\n"))
	if len(diags) != 1 || diags[0].Code != diag.CodeUnexpectedToken {
		t.Errorf("diags = \n%s", formatDiags(diags))
	}
}

func TestStatementAfterErrorStillParsed(t *testing.T) {
	f, diags := Parse("t.lotus", []byte("bad ]\ngood 1\n"))
	if len(diags) != 1 {
		t.Fatalf("diags = \n%s", formatDiags(diags))
	}
	if len(f.Statements) != 1 || directive(t, f.Statements[0]).Name != "good" {
		t.Errorf("statements = %+v", f.Statements)
	}
}

func TestErrorLimit(t *testing.T) {
	_, diags := Parse("t.lotus", []byte(strings.Repeat("a ]\n", 500)))
	errs := 0
	for _, d := range diags {
		if d.Severity == diag.Error {
			errs++
		}
	}
	if errs != diag.DefaultMaxErrors {
		t.Errorf("got %d errors, want %d", errs, diag.DefaultMaxErrors)
	}
	if last := diags[len(diags)-1]; last.Code != diag.CodeTooManyErrors {
		t.Errorf("last diagnostic = %+v", last)
	}
}

func TestFileTooLarge(t *testing.T) {
	_, diags := Parse("big.lotus", make([]byte, MaxFileSize+1))
	if len(diags) != 1 || diags[0].Code != diag.CodeFileTooLarge {
		t.Errorf("diags = %v", diags)
	}
	// Exactly at the limit is allowed (this is all NULs, so only lexical errors).
	if _, diags := Parse("ok.lotus", []byte(strings.Repeat("#", MaxFileSize))); len(diags) != 0 {
		t.Errorf("a file of exactly MaxFileSize bytes was rejected: %v", diags)
	}
}

func TestDeepNestingDoesNotOverflowStack(t *testing.T) {
	for name, src := range map[string]string{
		"blocks": strings.Repeat("a {\n", 100000),
		"lists":  "a " + strings.Repeat("[", 100000),
		"calls":  "a " + strings.Repeat("f(", 100000),
	} {
		t.Run(name, func(t *testing.T) {
			_, diags := Parse("deep.lotus", []byte(src))
			if len(diags) == 0 || diags[0].Code != diag.CodeNestingTooDeep {
				t.Errorf("first diagnostic = %v, want %s", diags, diag.CodeNestingTooDeep)
			}
		})
	}
}

func TestEmptyAndWhitespaceOnlyFiles(t *testing.T) {
	for _, src := range []string{"", "\n\n", "   \t\n", "# only a comment\n", ";;;\n"} {
		f := mustParse(t, src)
		if len(f.Statements) != 0 {
			t.Errorf("%q: got statements %v", src, f.Statements)
		}
	}
}

func TestPositionsAreFileRelative(t *testing.T) {
	f, _ := Parse("conf/main.lotus", []byte("a 1\n  b 2\n"))
	b := directive(t, f.Statements[1])
	if b.Pos.File != "conf/main.lotus" || b.Pos.Line != 2 || b.Pos.Col != 3 {
		t.Errorf("b.Pos = %+v", b.Pos)
	}
	if got := b.Pos.String(); got != "conf/main.lotus:2:3" {
		t.Errorf("Pos.String() = %q", got)
	}
}

// BenchmarkParseLarge parses a ~8 MiB file of typical content (nested sites,
// comments, lists) to catch accidental quadratic behavior.
func BenchmarkParseLarge(b *testing.B) {
	var sb strings.Builder
	sb.WriteString("lotus_config_version 1\n")
	for i := 0; sb.Len() < 8<<20; i++ {
		sb.WriteString(`# site block
site "example.com" {
    aliases [ "www.example.com", "a.example.com" ]
    root "/var/www"  # trailing
    route ~ '\.php$' {
        php "php83"
    }
}

`)
	}
	src := []byte(sb.String())
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, diags := Parse("big.lotus", src); len(diags) != 0 {
			b.Fatal(diags[0])
		}
	}
}

// BenchmarkParseManyComments guards the comment bookkeeping against quadratic growth.
func BenchmarkParseManyComments(b *testing.B) {
	src := []byte(strings.Repeat("# c\n", 1<<20) + "a 1\n")
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		if _, diags := Parse("c.lotus", src); len(diags) != 0 {
			b.Fatal(diags[0])
		}
	}
}
