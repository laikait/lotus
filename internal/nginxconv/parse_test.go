package nginxconv

import (
	"strings"
	"testing"
)

func parseText(t *testing.T, src string, extra ...map[string]string) ([]*Node, error) {
	t.Helper()
	files := map[string]string{"nginx.conf": src}
	for _, m := range extra {
		for k, v := range m {
			files[k] = v
		}
	}
	return Parse(write(t, files), Options{})
}

func TestParseStructure(t *testing.T) {
	nodes, err := parseText(t, `
# a comment
worker_processes 4;   # trailing comment
http {
    server {
        listen 80;
        location / { return 200 "ok"; }
    }
}
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 || nodes[0].Name != "worker_processes" || nodes[0].Args[0] != "4" || nodes[0].Line != 3 {
		t.Fatalf("%+v", nodes)
	}
	srv := nodes[1].Block[0]
	if srv.Name != "server" || !srv.IsCtx || len(srv.Block) != 2 {
		t.Fatalf("%+v", srv)
	}
	loc := srv.Block[1]
	if loc.Name != "location" || loc.Args[0] != "/" || loc.Block[0].Name != "return" || loc.Block[0].Args[1] != "ok" || loc.Block[0].Line != 7 {
		t.Fatalf("%+v %+v", loc, loc.Block[0])
	}
}

func TestParseQuoting(t *testing.T) {
	nodes, err := parseText(t, `a "two words" 'single; with ; and { braces }' "esc \"q\" \\ \t" plain\x ${var} a${b}c;
b "";
location ~ "^/(\d{4})/" { }
`)
	if err != nil {
		t.Fatal(err)
	}
	a := nodes[0].Args
	want := []string{"two words", "single; with ; and { braces }", "esc \"q\" \\ \t", `plain\x`, "${var}", "a${b}c"}
	if strings.Join(a, "|") != strings.Join(want, "|") {
		t.Errorf("args = %q, want %q", a, want)
	}
	if len(nodes[1].Args) != 1 || nodes[1].Args[0] != "" {
		t.Errorf("an empty string is an argument: %q", nodes[1].Args)
	}
	if nodes[2].Args[1] != `^/(\d{4})/` {
		t.Errorf("a backslash that is not an escape stays: %q", nodes[2].Args[1])
	}
}

func TestParseCommentsInsideWords(t *testing.T) {
	nodes, err := parseText(t, "a b#c d;\n# whole line\ne f; # trailing\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 || strings.Join(nodes[0].Args, "|") != "b#c|d" {
		t.Errorf("a # inside a word is part of it: %+v", nodes[0])
	}
}

func TestParseErrors(t *testing.T) {
	for name, src := range map[string]string{
		"unclosed block":  "http { server {",
		"stray brace":     "http { } }",
		"unclosed string": `a "never closed;`,
		"missing semi":    "a b c",
		"lone brace":      "{ }",
		"semi after word": "a b {",
	} {
		if _, err := parseText(t, src); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := parseText(t, "a \"x\n"); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Errorf("the error names the line: %v", err)
	}
}

func TestParseEmptyStatements(t *testing.T) {
	nodes, err := parseText(t, ";; a b;; ;")
	if err != nil || len(nodes) != 1 {
		t.Fatalf("%v %+v", err, nodes)
	}
}

func TestParseBOM(t *testing.T) {
	nodes, err := parseText(t, "\xef\xbb\xbfa b;")
	if err != nil || len(nodes) != 1 || nodes[0].Name != "a" {
		t.Fatalf("%v %+v", err, nodes)
	}
}

func TestIncludeGlobOrderAndRelativePaths(t *testing.T) {
	nodes, err := parseText(t, "include conf.d/*.conf;", map[string]string{
		"conf.d/20-b.conf": "b 1;", "conf.d/10-a.conf": "a 1;", "conf.d/30-c.conf": "c 1;", "conf.d/skip.txt": "no 1;",
	})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, n := range nodes {
		names = append(names, n.Name)
	}
	if strings.Join(names, "") != "abc" {
		t.Errorf("included in sorted order: %v", names)
	}
	if !strings.HasSuffix(nodes[0].File, "10-a.conf") || nodes[0].Line != 1 {
		t.Errorf("positions point into the included file: %+v", nodes[0])
	}
}

func TestIncludeOfBuiltInFilesStaysAsANode(t *testing.T) {
	nodes, err := parseText(t, "include mime.types; include /etc/nginx/fastcgi_params; include proxy_params;")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 || nodes[0].Name != "include" {
		t.Errorf("%+v", nodes)
	}
}

func TestIncludeDepthLimit(t *testing.T) {
	files := map[string]string{"nginx.conf": "include l0.conf;"}
	for i := 0; i < 30; i++ {
		files["l"+itoa(i)+".conf"] = "include l" + itoa(i+1) + ".conf;"
	}
	files["l30.conf"] = "x 1;"
	nodes, err := parseText(t, "include l0.conf;", files)
	if err != nil {
		t.Fatal(err)
	}
	res := Convert(nodes, false)
	if issue(res, "include", Unsupported, "could not read") == nil {
		t.Errorf("a chain deeper than the limit is reported: %+v", res.Issues)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func TestParseMissingMainFile(t *testing.T) {
	if _, err := Parse("/no/such/nginx.conf", Options{}); err == nil {
		t.Error("expected an error")
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{sample, "a b;", "a { b; }", `a "x" 'y';`, "${", "a ${b", "{{{", "}}}", `"`, "a\x00b;", "include x;"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		toks, err := lex(src)
		if err != nil {
			return
		}
		p := &parser{opts: Options{ReadFile: func(string) ([]byte, error) { return nil, errNoFile }, Glob: func(string) ([]string, error) { return nil, nil }}}
		pos := 0
		nodes, err := p.block("f", toks, &pos, 0, false)
		if err != nil {
			return
		}
		// Whatever parses must convert without panicking, and its output must be text.
		res := Convert(nodes, true)
		if !strings.HasPrefix(res.Config, "#") {
			t.Fatal("no output")
		}
	})
}

type fileErr string

func (e fileErr) Error() string { return string(e) }

const errNoFile = fileErr("no such file")
