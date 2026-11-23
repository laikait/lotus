package loader

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/laikait/lotus/internal/config/ast"
	"github.com/laikait/lotus/internal/config/diag"
)

func env(name string) (string, bool) {
	vals := map[string]string{"HOME_DIR": "/home/lotus", "TOKEN": "s3cret-value"}
	v, ok := vals[name]
	return v, ok
}

func load(files map[string]string, mutate ...func(*Options)) (*Result, []diag.Diagnostic) {
	opts := Options{FS: MemFS{Files: files}, Getenv: env}
	for _, m := range mutate {
		m(&opts)
	}
	return Load("main.lotus", opts)
}

func codes(diags []diag.Diagnostic) string {
	var out []string
	for _, d := range diags {
		if d.Severity == diag.Error {
			out = append(out, d.Code)
		}
	}
	return strings.Join(out, ",")
}

func text(ds []diag.Diagnostic) string {
	var b strings.Builder
	for _, d := range ds {
		b.WriteString(d.Error() + "\n")
	}
	return b.String()
}

// names lists the directive names in a statement list, depth-first.
func names(stmts []ast.Statement) []string {
	var out []string
	for _, s := range stmts {
		if d, ok := s.(*ast.Directive); ok {
			out = append(out, d.Name)
			if d.Block != nil {
				out = append(out, "{")
				out = append(out, names(d.Block.Statements)...)
				out = append(out, "}")
			}
		}
	}
	return out
}

func mustLoad(t *testing.T, files map[string]string, mutate ...func(*Options)) *Result {
	t.Helper()
	res, diags := load(files, mutate...)
	if res == nil {
		t.Fatalf("load failed:\n%s", text(diags))
	}
	return res
}

func TestVersion(t *testing.T) {
	res := mustLoad(t, map[string]string{"main.lotus": "lotus_config_version 1\na 1\n"})
	if res.Version != 1 {
		t.Errorf("version = %d", res.Version)
	}
	if got := strings.Join(names(res.File.Statements), " "); got != "a" {
		t.Errorf("version line must be removed from the tree; got %q", got)
	}

	tests := []struct {
		name, src, code string
	}{
		{"missing", "a 1\n", diag.CodeMissingVersion},
		{"empty file", "", diag.CodeMissingVersion},
		{"not first", "a 1\nlotus_config_version 1\n", diag.CodeVersionMisplaced},
		{"too new", "lotus_config_version 2\n", diag.CodeUnsupportedVersion},
		{"too old", "lotus_config_version 0\n", diag.CodeUnsupportedVersion},
		{"not an integer", "lotus_config_version \"1\"\n", diag.CodeMissingVersion},
		{"declared twice", "lotus_config_version 1\nlotus_config_version 1\n", diag.CodeVersionMisplaced},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, diags := load(map[string]string{"main.lotus": tt.src})
			if res != nil || codes(diags) != tt.code {
				t.Errorf("codes = %q, want %q\n%s", codes(diags), tt.code, text(diags))
			}
		})
	}
}

func TestVersionInIncludedFile(t *testing.T) {
	_, diags := load(map[string]string{
		"main.lotus": "lotus_config_version 1\ninclude \"b.lotus\"\n",
		"b.lotus":    "lotus_config_version 1\n",
	})
	if codes(diags) != diag.CodeVersionMisplaced {
		t.Errorf("diags:\n%s", text(diags))
	}
}

func TestMissingRootFile(t *testing.T) {
	res, diags := Load("nope.lotus", Options{FS: MemFS{}})
	if res != nil || codes(diags) != diag.CodeIncludeNotFound {
		t.Errorf("diags:\n%s", text(diags))
	}
}

func TestIncludeSplicesInPlace(t *testing.T) {
	res := mustLoad(t, map[string]string{
		"main.lotus":  "lotus_config_version 1\nfirst 1\nblock {\n  include \"inner.lotus\"\n  after 1\n}\ninclude \"b.lotus\"\nlast 1\n",
		"inner.lotus": "inner 1\n",
		"b.lotus":     "b 1\n",
	})
	want := "first block { inner after } b last"
	if got := strings.Join(names(res.File.Statements), " "); got != want {
		t.Errorf("order = %q, want %q", got, want)
	}
	if len(res.Files) != 3 || res.Files[0] != "main.lotus" {
		t.Errorf("files = %v", res.Files)
	}
}

func TestIncludePositionsKeepTheirFile(t *testing.T) {
	res := mustLoad(t, map[string]string{
		"main.lotus":  "lotus_config_version 1\ninclude \"sub/b.lotus\"\n",
		"sub/b.lotus": "\n\n  x 1\n",
	})
	d := res.File.Statements[0].(*ast.Directive)
	if filepath.ToSlash(d.Pos.File) != "sub/b.lotus" || d.Pos.Line != 3 || d.Pos.Col != 3 {
		t.Errorf("pos = %v, want sub/b.lotus:3:3", d.Pos)
	}
}

func TestIncludeRelativeToIncludingFile(t *testing.T) {
	res := mustLoad(t, map[string]string{
		"main.lotus":  "lotus_config_version 1\ninclude \"a/one.lotus\"\n",
		"a/one.lotus": "one 1\ninclude \"two.lotus\"\n", // relative to a/, not to the root
		"a/two.lotus": "two 1\n",
		"two.lotus":   "wrong 1\n",
	})
	if got := strings.Join(names(res.File.Statements), " "); got != "one two" {
		t.Errorf("got %q", got)
	}
}

func TestIncludeGlob(t *testing.T) {
	res := mustLoad(t, map[string]string{
		"main.lotus":       "lotus_config_version 1\ninclude \"sites.d/*.lotus\"\n",
		"sites.d/b.lotus":  "b 1\n",
		"sites.d/a.lotus":  "a 1\n",
		"sites.d/c.lotus":  "c 1\n",
		"sites.d/skip.txt": "ignored 1\n",
	})
	if got := strings.Join(names(res.File.Statements), " "); got != "a b c" {
		t.Errorf("glob order = %q, want sorted a b c", got)
	}

	// A glob matching nothing is fine.
	res = mustLoad(t, map[string]string{"main.lotus": "lotus_config_version 1\ninclude \"none.d/*.lotus\"\nx 1\n"})
	if got := strings.Join(names(res.File.Statements), " "); got != "x" {
		t.Errorf("got %q", got)
	}
}

func TestIncludeErrors(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		code  string
	}{
		{"missing file", map[string]string{"main.lotus": "lotus_config_version 1\ninclude \"nope.lotus\"\n"}, diag.CodeIncludeNotFound},
		{"wildcard in directory", map[string]string{
			"main.lotus": "lotus_config_version 1\ninclude \"*/x.lotus\"\n", "a/x.lotus": "x 1\n"}, diag.CodeBadIncludePath},
		{"empty path", map[string]string{"main.lotus": "lotus_config_version 1\ninclude \"\"\n"}, diag.CodeBadIncludePath},
		{"self include", map[string]string{"main.lotus": "lotus_config_version 1\ninclude \"main.lotus\"\n"}, diag.CodeIncludeCycle},
		{"indirect cycle", map[string]string{
			"main.lotus": "lotus_config_version 1\ninclude \"a.lotus\"\n",
			"a.lotus":    "include \"b.lotus\"\n",
			"b.lotus":    "include \"a.lotus\"\n"}, diag.CodeIncludeCycle},
		{"cycle through a different spelling", map[string]string{
			"main.lotus": "lotus_config_version 1\ninclude \"a/../main.lotus\"\n"}, diag.CodeIncludeCycle},
		{"syntax error in included file", map[string]string{
			"main.lotus": "lotus_config_version 1\ninclude \"a.lotus\"\n", "a.lotus": "x \"unterminated\n"}, diag.CodeUnterminatedString},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, diags := load(tt.files)
			if res != nil || codes(diags) != tt.code {
				t.Errorf("codes = %q, want %q\n%s", codes(diags), tt.code, text(diags))
			}
		})
	}
}

func TestSameFileIncludedTwiceIsNotACycle(t *testing.T) {
	res := mustLoad(t, map[string]string{
		"main.lotus": "lotus_config_version 1\ninclude \"a.lotus\"\ninclude \"a.lotus\"\n",
		"a.lotus":    "x 1\n",
	})
	if got := strings.Join(names(res.File.Statements), " "); got != "x x" {
		t.Errorf("got %q", got)
	}
}

func TestIncludeLimits(t *testing.T) {
	// Depth: main -> f1 -> f2 -> f3, limit 2.
	files := map[string]string{
		"main.lotus": "lotus_config_version 1\ninclude \"f1.lotus\"\n",
		"f1.lotus":   "include \"f2.lotus\"\n",
		"f2.lotus":   "include \"f3.lotus\"\n",
		"f3.lotus":   "x 1\n",
	}
	_, diags := load(files, func(o *Options) { o.MaxIncludeDepth = 2 })
	if codes(diags) != diag.CodeIncludeDepth {
		t.Errorf("depth: %q\n%s", codes(diags), text(diags))
	}
	if _, diags := load(files, func(o *Options) { o.MaxIncludeDepth = 3 }); len(diags) != 0 {
		t.Errorf("depth 3 should be allowed:\n%s", text(diags))
	}

	// File count: root + 3 matches with a limit of 3 files.
	files = map[string]string{
		"main.lotus": "lotus_config_version 1\ninclude \"d/*.lotus\"\n",
		"d/1.lotus":  "a 1\n", "d/2.lotus": "a 1\n", "d/3.lotus": "a 1\n",
	}
	_, diags = load(files, func(o *Options) { o.MaxFiles = 3 })
	if codes(diags) != diag.CodeTooManyFiles {
		t.Errorf("file count: %q\n%s", codes(diags), text(diags))
	}
}

func TestInsecureIncludes(t *testing.T) {
	files := map[string]string{
		"main.lotus": "lotus_config_version 1\ninclude \"a.lotus\"\n",
		"a.lotus":    "x 1\n",
	}
	unsafe := func(o *Options) {
		o.FS = MemFS{Files: files, Unsafe: map[string]string{"a.lotus": "a.lotus is writable by everyone"}}
	}
	res, diags := load(files, unsafe)
	if res != nil || codes(diags) != diag.CodeInsecureInclude {
		t.Fatalf("diags:\n%s", text(diags))
	}
	if !strings.Contains(diags[0].Message, "writable by everyone") {
		t.Errorf("message = %q", diags[0].Message)
	}
	if res, diags := load(files, unsafe, func(o *Options) { o.InsecureIncludes = true }); res == nil {
		t.Errorf("InsecureIncludes should skip the check:\n%s", text(diags))
	}
}

func TestVariables(t *testing.T) {
	res := mustLoad(t, map[string]string{
		"main.lotus": `lotus_config_version 1
let $root = "/var/www"
let $n = 5
let $names = [ "a", "b" ]
let $full = "${root}/site-${n}"
d $full "${root}" $names $n
include "b.lotus"
`,
		"b.lotus": "let $local = \"x\"\nfrom_b \"${root}/${local}\"\n",
	})
	d := res.File.Statements[0].(*ast.Directive)
	if s, _ := d.Args[0].(*ast.String).Plain(); s != "/var/www/site-5" {
		t.Errorf("chained interpolation = %q", s)
	}
	if s, _ := d.Args[1].(*ast.String).Plain(); s != "/var/www" {
		t.Errorf("interpolation = %q", s)
	}
	if l, ok := d.Args[2].(*ast.List); !ok || len(l.Items) != 2 {
		t.Errorf("list variable = %#v", d.Args[2])
	}
	if n, ok := d.Args[3].(*ast.Int); !ok || n.Value != 5 {
		t.Errorf("int variable = %#v", d.Args[3])
	}
	b := res.File.Statements[1].(*ast.Directive)
	if s, _ := b.Args[0].(*ast.String).Plain(); s != "/var/www/x" {
		t.Errorf("included file sees parent variables and its own: %q", s)
	}
}

func TestVariableUsePositionIsTheUse(t *testing.T) {
	res := mustLoad(t, map[string]string{"main.lotus": "lotus_config_version 1\nlet $a = 1\nd $a\n"})
	d := res.File.Statements[0].(*ast.Directive)
	if p := d.Args[0].Position(); p.Line != 3 || p.Col != 3 {
		t.Errorf("position = %v, want 3:3 (the use)", p)
	}
}

func TestVariableScoping(t *testing.T) {
	// A variable defined in an included file is not visible to the includer.
	_, diags := load(map[string]string{
		"main.lotus": "lotus_config_version 1\ninclude \"a.lotus\"\nd $inner\n",
		"a.lotus":    "let $inner = 1\n",
	})
	if codes(diags) != diag.CodeUndefinedVariable {
		t.Errorf("diags:\n%s", text(diags))
	}
	// A variable defined after the include is not visible inside it.
	_, diags = load(map[string]string{
		"main.lotus": "lotus_config_version 1\ninclude \"a.lotus\"\nlet $later = 1\n",
		"a.lotus":    "d $later\n",
	})
	if codes(diags) != diag.CodeUndefinedVariable {
		t.Errorf("diags:\n%s", text(diags))
	}
}

func TestVariableErrors(t *testing.T) {
	tests := []struct {
		name, body, code string
	}{
		{"undefined", `d $nope`, diag.CodeUndefinedVariable},
		{"undefined in string", `d "${nope}"`, diag.CodeUndefinedVariable},
		{"use before definition", "d $a\nlet $a = 1", diag.CodeUndefinedVariable},
		{"redefined", "let $a = 1\nlet $a = 2", diag.CodeVariableRedefined},
		{"list inside string", "let $l = [ \"a\" ]\nd \"x${l}\"", diag.CodeVariableType},
		{"null inside string", "let $n = null\nd \"x${n}\"", diag.CodeVariableType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, diags := load(map[string]string{"main.lotus": "lotus_config_version 1\n" + tt.body + "\n"})
			if res != nil || codes(diags) != tt.code {
				t.Errorf("codes = %q, want %q\n%s", codes(diags), tt.code, text(diags))
			}
		})
	}
}

func TestUndefinedVariableSuggestion(t *testing.T) {
	_, diags := load(map[string]string{"main.lotus": "lotus_config_version 1\nlet $webroot = \"/w\"\nd $webrot\n"})
	if len(diags) != 1 || !strings.Contains(diags[0].Hint, "$webroot") {
		t.Errorf("diags:\n%s", text(diags))
	}
}

func TestRedefinitionInIncludedFileIsRejected(t *testing.T) {
	_, diags := load(map[string]string{
		"main.lotus": "lotus_config_version 1\nlet $a = 1\ninclude \"a.lotus\"\n",
		"a.lotus":    "let $a = 2\n",
	})
	if codes(diags) != diag.CodeVariableRedefined {
		t.Errorf("diags:\n%s", text(diags))
	}
}

func TestEnvFunction(t *testing.T) {
	res := mustLoad(t, map[string]string{"main.lotus": "lotus_config_version 1\nd env(\"HOME_DIR\") env(\"MISSING\", \"fallback\")\n"})
	d := res.File.Statements[0].(*ast.Directive)
	if s, _ := d.Args[0].(*ast.String).Plain(); s != "/home/lotus" {
		t.Errorf("env = %q", s)
	}
	if s, _ := d.Args[1].(*ast.String).Plain(); s != "fallback" {
		t.Errorf("env default = %q", s)
	}
	for i, a := range d.Args {
		if !a.(*ast.String).Secret {
			t.Errorf("arg %d: values from env() must be marked secret", i)
		}
	}
}

func TestEnvErrors(t *testing.T) {
	tests := []struct {
		name, body, code string
	}{
		{"not set", `d env("MISSING")`, diag.CodeEnvNotSet},
		{"no arguments", `d env()`, diag.CodeBadFunction},
		{"too many arguments", `d env("A", "b", "c")`, diag.CodeBadFunction},
		{"non-string argument", `d env(5)`, diag.CodeBadFunction},
		{"nested call", `d env(env("HOME_DIR"))`, diag.CodeBadFunction},
		{"unknown function", `d envv("HOME_DIR")`, diag.CodeBadFunction},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, diags := load(map[string]string{"main.lotus": "lotus_config_version 1\n" + tt.body + "\n"})
			if res != nil || codes(diags) != tt.code {
				t.Errorf("codes = %q, want %q\n%s", codes(diags), tt.code, text(diags))
			}
		})
	}
	_, diags := load(map[string]string{"main.lotus": "lotus_config_version 1\nd envv(\"X\")\n"})
	if len(diags) != 1 || !strings.Contains(diags[0].Hint, `"env"`) {
		t.Errorf("expected a did-you-mean hint:\n%s", text(diags))
	}
}

func TestFileFunction(t *testing.T) {
	res := mustLoad(t, map[string]string{
		"main.lotus":     "lotus_config_version 1\ninclude \"conf/a.lotus\"\n",
		"conf/a.lotus":   "d file(\"token.txt\")\n", // relative to the file that calls it
		"conf/token.txt": "tok-123\r\n",
	})
	s := res.File.Statements[0].(*ast.Directive).Args[0].(*ast.String)
	if v, _ := s.Plain(); v != "tok-123" {
		t.Errorf("file() = %q, want the content without its trailing newline", v)
	}
	if !s.Secret {
		t.Error("file() values must be secret")
	}

	tests := []struct {
		name  string
		files map[string]string
	}{
		{"missing", map[string]string{"main.lotus": "lotus_config_version 1\nd file(\"nope\")\n"}},
		{"too large", map[string]string{
			"main.lotus": "lotus_config_version 1\nd file(\"big\")\n", "big": strings.Repeat("x", 64<<10+1)}},
		{"not utf-8", map[string]string{"main.lotus": "lotus_config_version 1\nd file(\"bin\")\n", "bin": "\xff\xfe"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, diags := load(tt.files)
			if res != nil || codes(diags) != diag.CodeFileFunction {
				t.Errorf("codes = %q\n%s", codes(diags), text(diags))
			}
		})
	}
}

func TestSecretsPropagateAndAreRedacted(t *testing.T) {
	res := mustLoad(t, map[string]string{"main.lotus": `lotus_config_version 1
let $t = env("TOKEN")
d "Bearer ${t}" $t "plain"
`})
	d := res.File.Statements[0].(*ast.Directive)
	for i, want := range []bool{true, true, false} {
		if got := d.Args[i].(*ast.String).Secret; got != want {
			t.Errorf("arg %d secret = %v, want %v", i, got, want)
		}
	}
	dump := ast.Dump(res.File)
	if strings.Contains(dump, "s3cret-value") {
		t.Errorf("dump leaks a secret:\n%s", dump)
	}
	if !strings.Contains(dump, "secret redacted") {
		t.Errorf("dump should say a value is redacted:\n%s", dump)
	}
}

func TestEnvTextIsNeverAPlaceholder(t *testing.T) {
	// Text from env() must not be scanned for {placeholders}.
	res := mustLoad(t, map[string]string{"main.lotus": "lotus_config_version 1\nd env(\"MISSING\", \"{uri}\")\n"})
	s := res.File.Statements[0].(*ast.Directive).Args[0].(*ast.String)
	for _, p := range s.Parts {
		if !p.Escaped {
			t.Errorf("part %+v must be marked literal", p)
		}
	}
}

func TestPairsAreResolved(t *testing.T) {
	res := mustLoad(t, map[string]string{"main.lotus": "lotus_config_version 1\nlet $v = \"x\"\nh {\n \"K\" = \"${v}-1\"\n}\n"})
	pair := res.File.Statements[0].(*ast.Directive).Block.Statements[0].(*ast.Pair)
	if v, _ := pair.Value.(*ast.String).Plain(); v != "x-1" {
		t.Errorf("pair value = %q", v)
	}
}

func TestErrorsFromSeveralFilesAreCollected(t *testing.T) {
	_, diags := load(map[string]string{
		"main.lotus": "lotus_config_version 1\nd $nope\ninclude \"a.lotus\"\ninclude \"b.lotus\"\n",
		"a.lotus":    "e env(\"MISSING\")\n",
		"b.lotus":    "f \"\\q\"\n",
	})
	if got := codes(diags); got != diag.CodeUndefinedVariable+","+diag.CodeEnvNotSet+","+diag.CodeInvalidEscape {
		t.Errorf("codes = %q\n%s", got, text(diags))
	}
}

func TestMemFSGlob(t *testing.T) {
	m := MemFS{Files: map[string]string{"d/a.lotus": "", "d/b.txt": "", "e/c.lotus": ""}}
	got, _ := m.Glob("d/*.lotus")
	if len(got) != 1 || got[0] != "d/a.lotus" {
		t.Errorf("glob = %v", got)
	}
}
