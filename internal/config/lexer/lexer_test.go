package lexer

import (
	"strings"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/config/ast"
	"github.com/laikait/lotus/internal/config/diag"
)

func lexAll(src string) ([]Token, []diag.Diagnostic) {
	diags := diag.NewList(diag.DefaultMaxErrors)
	l := New("t.lotus", src, diags)
	var toks []Token
	for {
		t := l.Next()
		if t.Kind == EOF {
			return toks, diags.Items()
		}
		toks = append(toks, t)
	}
}

func kinds(toks []Token) []Kind {
	out := make([]Kind, len(toks))
	for i, t := range toks {
		out[i] = t.Kind
	}
	return out
}

func TestNumbers(t *testing.T) {
	tests := []struct {
		src  string
		kind Kind
		num  int64
	}{
		{"0", Integer, 0},
		{"10", Integer, 10},
		{"10_000", Integer, 10000},
		{"100mb", Size, 100_000_000},
		{"100mib", Size, 100 << 20},
		{"1gib", Size, 1 << 30},
		{"512b", Size, 512},
		{"15s", Duration, int64(15 * time.Second)},
		{"10ms", Duration, int64(10 * time.Millisecond)},
		{"5m", Duration, int64(5 * time.Minute)},
		{"1h30m", Duration, int64(90 * time.Minute)},
		{"2d12h", Duration, int64(60 * time.Hour)},
		{"1m30s500ms", Duration, int64(90*time.Second + 500*time.Millisecond)},
	}
	for _, tt := range tests {
		toks, diags := lexAll(tt.src)
		if len(diags) != 0 || len(toks) != 1 {
			t.Errorf("%q: toks=%v diags=%v", tt.src, toks, diags)
			continue
		}
		if toks[0].Kind != tt.kind || toks[0].Num != tt.num {
			t.Errorf("%q: got kind %v num %d, want kind %v num %d", tt.src, toks[0].Kind, toks[0].Num, tt.kind, tt.num)
		}
	}
}

func TestInvalidNumbers(t *testing.T) {
	tests := []struct {
		src  string
		code string
	}{
		{"30m1h", diag.CodeInvalidNumber}, // wrong unit order
		{"1h1h", diag.CodeInvalidNumber},  // repeated unit
		{"1_", diag.CodeInvalidNumber},    // trailing underscore
		{"1__0", diag.CodeInvalidNumber},  // double underscore
		{"1_mb", diag.CodeInvalidNumber},  // underscore before unit
		{"10xs", diag.CodeInvalidNumber},  // unknown unit
		{"10MB", diag.CodeInvalidNumber},  // uppercase unit
		{"1h30", diag.CodeInvalidNumber},  // missing final unit
		{"99999999999999999999", diag.CodeNumberRange},
		{"9223372036854775807kb", diag.CodeNumberRange},
		{"9999999999999d", diag.CodeNumberRange},
	}
	for _, tt := range tests {
		toks, diags := lexAll(tt.src)
		if len(diags) != 1 || diags[0].Code != tt.code {
			t.Errorf("%q: diags=%v, want one %s", tt.src, diags, tt.code)
		}
		if len(toks) != 1 || toks[0].Kind != Invalid {
			t.Errorf("%q: want one Invalid token, got %v", tt.src, toks)
		}
	}
	// The hint for an uppercase unit says to use lowercase.
	_, diags := lexAll("10MB")
	if !strings.Contains(diags[0].Hint, "mb") {
		t.Errorf("hint = %q, want it to suggest mb", diags[0].Hint)
	}
}

func TestStrings(t *testing.T) {
	tests := []struct {
		src   string
		kind  Kind
		parts []ast.StringPart
	}{
		{`"abc"`, String, []ast.StringPart{{Text: "abc"}}},
		{`""`, String, nil},
		{`"a\nb\t\"\\\$"`, String, []ast.StringPart{{Text: "a\nb\t\"\\$"}}},
		{`"x${y}z"`, String, []ast.StringPart{{Text: "x"}, {Kind: ast.PartVar, Var: "y"}, {Text: "z"}}},
		{`"{uri}"`, String, []ast.StringPart{{Text: "{uri}"}}},
		{`"\{uri\}"`, String, []ast.StringPart{{Text: "{", Escaped: true}, {Text: "uri"}, {Text: "}", Escaped: true}}},
		{`"price$"`, String, []ast.StringPart{{Text: "price$"}}},
		{`"\u{1F600}"`, String, []ast.StringPart{{Text: "\U0001F600"}}},
		{`'\.php$'`, RawString, []ast.StringPart{{Text: `\.php$`}}},
		{`'C:\LOTUS\www'`, RawString, []ast.StringPart{{Text: `C:\LOTUS\www`}}},
		{`'${not_a_var}'`, RawString, []ast.StringPart{{Text: "${not_a_var}"}}},
	}
	for _, tt := range tests {
		toks, diags := lexAll(tt.src)
		if len(diags) != 0 || len(toks) != 1 || toks[0].Kind != tt.kind {
			t.Errorf("%s: toks=%v diags=%v", tt.src, toks, diags)
			continue
		}
		got := toks[0].Parts
		if len(got) != len(tt.parts) {
			t.Errorf("%s: parts = %+v, want %+v", tt.src, got, tt.parts)
			continue
		}
		for i := range got {
			if got[i] != tt.parts[i] {
				t.Errorf("%s: part %d = %+v, want %+v", tt.src, i, got[i], tt.parts[i])
			}
		}
		if toks[0].Text != tt.src {
			t.Errorf("%s: source text = %q", tt.src, toks[0].Text)
		}
	}
}

func TestInvalidStrings(t *testing.T) {
	tests := []struct {
		src  string
		code string
	}{
		{`"abc`, diag.CodeUnterminatedString},
		{`'abc`, diag.CodeUnterminatedString},
		{`"\q"`, diag.CodeInvalidEscape},
		{`"\."`, diag.CodeInvalidEscape},
		{`"\u{110000}"`, diag.CodeInvalidEscape},
		{`"\u{D800}"`, diag.CodeInvalidEscape},
		{`"\u{}"`, diag.CodeInvalidEscape},
		{`"\u{0}"`, diag.CodeInvalidEscape},
		{`"\u0041"`, diag.CodeInvalidEscape},
		{`"${}"`, diag.CodeBadInterpolation},
		{`"${Name}"`, diag.CodeBadInterpolation},
		{`"${1a}"`, diag.CodeBadInterpolation},
		{`"${abc"`, diag.CodeBadInterpolation},
		{"\"a\x01b\"", diag.CodeUnexpectedChar},
		{"'a\x01b'", diag.CodeUnexpectedChar},
	}
	for _, tt := range tests {
		toks, diags := lexAll(tt.src)
		if len(diags) == 0 || diags[0].Code != tt.code {
			t.Errorf("%q: diags=%v, want first %s", tt.src, diags, tt.code)
		}
		if len(toks) != 1 || toks[0].Kind != Invalid {
			t.Errorf("%q: want one Invalid token, got %v", tt.src, toks)
		}
	}
}

func TestStringCannotSpanLines(t *testing.T) {
	toks, diags := lexAll("\"a\nb\"")
	if len(diags) == 0 || diags[0].Code != diag.CodeUnterminatedString {
		t.Fatalf("diags = %v, want first %s", diags, diag.CodeUnterminatedString)
	}
	if diags[0].Pos.Line != 1 || diags[0].Pos.Col != 1 {
		t.Errorf("reported at %v, want the opening quote 1:1", diags[0].Pos)
	}
	// Lexing resumes on the next line.
	if len(toks) == 0 || toks[0].Kind != Invalid || toks[0].Pos.Line != 1 {
		t.Errorf("first token = %+v, want Invalid at line 1", toks)
	}
}

func TestOperators(t *testing.T) {
	toks, diags := lexAll(`= ~ ~* ^~ !~ !~*`)
	if len(diags) != 0 {
		t.Fatal(diags)
	}
	want := []string{"=", "~", "~*", "^~", "!~", "!~*"}
	if len(toks) != len(want) {
		t.Fatalf("got %d tokens", len(toks))
	}
	for i, w := range want {
		if toks[i].Kind != Operator || toks[i].Text != w {
			t.Errorf("token %d = %v %q, want operator %q", i, toks[i].Kind, toks[i].Text, w)
		}
	}
	for _, bad := range []string{"^x", "!x", "!"} {
		if _, diags := lexAll(bad); len(diags) != 1 || diags[0].Code != diag.CodeUnexpectedChar {
			t.Errorf("%q: diags = %v", bad, diags)
		}
	}
}

func TestIdentsAndVariables(t *testing.T) {
	toks, diags := lexAll("max_procs $webroot env(")
	if len(diags) != 0 {
		t.Fatal(diags)
	}
	if got, want := kinds(toks), []Kind{Ident, Variable, Ident, LParen}; !equalKinds(got, want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	if toks[1].Text != "webroot" {
		t.Errorf("variable text = %q", toks[1].Text)
	}
	if toks[0].Adjacent || !toks[2].Adjacent {
		t.Errorf("adjacent flags wrong: %v %v", toks[0].Adjacent, toks[2].Adjacent)
	}
	for _, bad := range []string{"Root", "maxProcs", "$Name", "$", "$1"} {
		if _, diags := lexAll(bad); len(diags) == 0 {
			t.Errorf("%q: expected an error", bad)
		}
	}
	// Whitespace before '(' means it's not a call.
	toks, _ = lexAll("env (")
	if toks[0].Adjacent {
		t.Error("ident followed by a space must not be adjacent")
	}
}

func equalKinds(a, b []Kind) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNewlinesAndBrackets(t *testing.T) {
	toks, diags := lexAll("a [\n 1,\n 2\n]\nb\n")
	if len(diags) != 0 {
		t.Fatal(diags)
	}
	want := []Kind{Ident, LBracket, Integer, Comma, Integer, RBracket, Newline, Ident, Newline}
	if got := kinds(toks); !equalKinds(got, want) {
		t.Errorf("kinds = %v, want %v", got, want)
	}

	// Parentheses behave the same way.
	toks, _ = lexAll("f(\n1\n)\n")
	want = []Kind{Ident, LParen, Integer, RParen, Newline}
	if got := kinds(toks); !equalKinds(got, want) {
		t.Errorf("kinds = %v, want %v", got, want)
	}
}

func TestCommentsAndSemicolons(t *testing.T) {
	toks, diags := lexAll("a 1 # note\nb 2; c 3\n")
	if len(diags) != 0 {
		t.Fatal(diags)
	}
	want := []Kind{Ident, Integer, Comment, Newline, Ident, Integer, Semicolon, Ident, Integer, Newline}
	if got := kinds(toks); !equalKinds(got, want) {
		t.Errorf("kinds = %v, want %v", got, want)
	}
	if toks[2].Text != "# note" {
		t.Errorf("comment text = %q", toks[2].Text)
	}
}

func TestCRLF(t *testing.T) {
	toks, diags := lexAll("a\r\nb\r\n")
	if len(diags) != 0 {
		t.Fatal(diags)
	}
	want := []Kind{Ident, Newline, Ident, Newline}
	if got := kinds(toks); !equalKinds(got, want) {
		t.Errorf("kinds = %v, want %v", got, want)
	}
	if toks[2].Pos.Line != 2 || toks[2].Pos.Col != 1 {
		t.Errorf("b at %v, want line 2 col 1", toks[2].Pos)
	}
	if _, diags := lexAll("a\rb"); len(diags) != 1 || diags[0].Code != diag.CodeUnexpectedChar {
		t.Errorf("bare CR: diags = %v", diags)
	}
}

func TestPositions(t *testing.T) {
	// "é" is two bytes but one column.
	toks, diags := lexAll("a\n  é b")
	if len(diags) != 1 { // 'é' can't start a token
		t.Fatalf("diags = %v", diags)
	}
	if diags[0].Pos.Line != 2 || diags[0].Pos.Col != 3 {
		t.Errorf("é reported at %v, want 2:3", diags[0].Pos)
	}
	b := toks[len(toks)-1]
	if b.Pos.Line != 2 || b.Pos.Col != 5 || b.Pos.Offset != 7 {
		t.Errorf("b at %+v, want line 2 col 5 offset 7", b.Pos)
	}
}

func TestBOM(t *testing.T) {
	toks, diags := lexAll("\xef\xbb\xbfa")
	if len(diags) != 0 || len(toks) != 1 {
		t.Fatalf("toks=%v diags=%v", toks, diags)
	}
	if toks[0].Pos.Line != 1 || toks[0].Pos.Col != 1 {
		t.Errorf("a at %v, want 1:1", toks[0].Pos)
	}
}

func TestInvalidUTF8(t *testing.T) {
	toks, diags := lexAll("a \xff b")
	if len(diags) != 1 || diags[0].Code != diag.CodeInvalidUTF8 {
		t.Fatalf("diags = %v", diags)
	}
	if diags[0].Pos.Line != 1 || diags[0].Pos.Col != 3 {
		t.Errorf("reported at %v, want 1:3", diags[0].Pos)
	}
	if len(toks) != 0 {
		t.Errorf("no tokens expected after invalid UTF-8, got %v", toks)
	}
}

func TestUnexpectedCharacters(t *testing.T) {
	for _, src := range []string{"@", "\x00", "%", "`", "\\"} {
		toks, diags := lexAll(src)
		if len(diags) != 1 || diags[0].Code != diag.CodeUnexpectedChar || len(toks) != 0 {
			t.Errorf("%q: toks=%v diags=%v", src, toks, diags)
		}
	}
}

func TestErrorLimitStopsLexing(t *testing.T) {
	_, diags := lexAll(strings.Repeat("@ ", 500))
	errs := 0
	for _, d := range diags {
		if d.Severity == diag.Error {
			errs++
		}
	}
	if errs != diag.DefaultMaxErrors {
		t.Errorf("got %d errors, want %d", errs, diag.DefaultMaxErrors)
	}
	if last := diags[len(diags)-1]; last.Code != diag.CodeTooManyErrors || last.Severity != diag.Note {
		t.Errorf("last diagnostic = %+v, want the too-many-errors note", last)
	}
}
