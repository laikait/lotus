package schema

import (
	"strings"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/config/ast"
)

func str(s string) *ast.String { return &ast.String{Parts: []ast.StringPart{{Text: s}}} }

// The default registry must be internally consistent and fully documented
// (docs/configuration/directive-template.md).
func TestDefaultRegistryIntegrity(t *testing.T) {
	r := Default()
	defined := map[string]bool{}
	r.All(func(ctx Context, d *Directive) {
		if d.Defines != "" {
			defined[d.Defines] = true
		}
	})
	count := 0
	r.All(func(ctx Context, d *Directive) {
		count++
		id := string(ctx) + "." + d.Name
		if strings.TrimSpace(d.Description) == "" {
			t.Errorf("%s: missing Description", id)
		}
		if strings.TrimSpace(d.Security) == "" {
			t.Errorf("%s: missing Security (write \"None.\" only if that is true)", id)
		}
		if d.Since == "" || d.Default == "" {
			t.Errorf("%s: missing Since or Default", id)
		}
		if d.Block != BlockNone {
			if r.Context(d.Opens) == nil {
				t.Errorf("%s: opens context %q, which is not defined", id, d.Opens)
			}
		} else if d.Opens != "" {
			t.Errorf("%s: has Opens but no block", id)
		}
		if d.Card == Keyed && (len(d.Args) == 0 || d.Args[0].Optional) {
			t.Errorf("%s: a keyed directive needs a required first argument as its key", id)
		}
		for i, a := range d.Args {
			if a.Type == nil {
				t.Errorf("%s: argument %d has no type", id, i)
			}
			if a.Variadic && i != len(d.Args)-1 {
				t.Errorf("%s: only the last argument can be variadic", id)
			}
			if a.Ref != "" && !defined[a.Ref] {
				t.Errorf("%s: refers to namespace %q that no directive defines", id, a.Ref)
			}
		}
	})
	if count < 30 {
		t.Errorf("only %d directives registered; the registry looks incomplete", count)
	}
	for _, name := range r.Context(CtxServer).Required {
		if r.Lookup(CtxServer, name) == nil {
			t.Errorf("server requires %q, which is not defined there", name)
		}
	}
}

func TestEveryContextIsReachable(t *testing.T) {
	r := Default()
	reachable := map[Context]bool{CtxRoot: true}
	for changed := true; changed; {
		changed = false
		r.All(func(ctx Context, d *Directive) {
			if reachable[ctx] && d.Block != BlockNone && !reachable[d.Opens] {
				reachable[d.Opens] = true
				changed = true
			}
		})
	}
	for _, ctx := range []Context{CtxServer, CtxRuntime, CtxListen, CtxLimits, CtxTLS, CtxACME, CtxSite, CtxRoute,
		CtxMatch, CtxLogs, CtxPHPBackend, CtxUpstream, CtxBackend, CtxHealthCheck, CtxSecurityHeaders, CtxControl} {
		if !reachable[ctx] {
			t.Errorf("context %q cannot be reached from the root", ctx)
		}
	}
}

func TestTypes(t *testing.T) {
	size := &ast.Size{Bytes: 1 << 20, Source: "1mib"}
	dur := &ast.Duration{Value: 5 * time.Second, Source: "5s"}
	tests := []struct {
		name string
		typ  Type
		val  ast.Value
		ok   bool
	}{
		{"string ok", String(), str("x"), true},
		{"string rejects int", String(), &ast.Int{Value: 1, Source: "1"}, false},
		{"bool ok", Bool(), &ast.Bool{Value: true}, true},
		{"bool rejects string", Bool(), str("true"), false},
		{"int in range", Int(1, 10), &ast.Int{Value: 10, Source: "10"}, true},
		{"int above range", Int(1, 10), &ast.Int{Value: 11, Source: "11"}, false},
		{"int at least", IntAtLeast(1), &ast.Int{Value: 0, Source: "0"}, false},
		{"size ok", Size(1), size, true},
		{"size too small", Size(2 << 20), size, false},
		{"size rejects int", Size(1), &ast.Int{Value: 5, Source: "5"}, false},
		{"duration ok", Duration(time.Second), dur, true},
		{"duration too short", Duration(time.Minute), dur, false},
		{"enum bare word", Enum("a", "b"), &ast.Ident{Name: "a"}, true},
		{"enum string", Enum("1.2", "1.3"), str("1.3"), true},
		{"enum rejects other", Enum("a", "b"), &ast.Ident{Name: "c"}, false},
		{"cidr v4", CIDR(), str("10.0.0.0/8"), true},
		{"cidr v6", CIDR(), str("2001:db8::/32"), true},
		{"cidr bare ip", CIDR(), str("192.0.2.1"), true},
		{"cidr garbage", CIDR(), str("10.0.0.0/33"), false},
		{"hostport", Address(HostPort), str("0.0.0.0:80"), true},
		{"hostport v6", Address(HostPort), str("[::1]:443"), true},
		{"hostport port only", Address(HostPort), str(":8080"), true},
		{"hostport hostname", Address(HostPort), str("backend.internal:9000"), true},
		{"hostport no port", Address(HostPort), str("0.0.0.0"), false},
		{"hostport port zero", Address(HostPort), str("0.0.0.0:0"), false},
		{"hostport bad host", Address(HostPort), str("bad host:80"), false},
		{"unix allowed", Address(HostPort | UnixSock), str("unix:/run/p.sock"), true},
		{"unix not allowed", Address(HostPort), str("unix:/run/p.sock"), false},
		{"unix empty", Address(UnixSock), str("unix:"), false},
		{"path ok", Path(), str("/var/www"), true},
		{"path empty", Path(), str(""), false},
		{"regex ok", Regex(), str(`^/a{2}$`), true},
		{"regex backreference unsupported", Regex(), str(`(a)\1`), false},
		{"regex lookahead unsupported", Regex(), str(`a(?=b)`), false},
		{"list ok", List(String(), 1), &ast.List{Items: []ast.Value{str("a")}}, true},
		{"list too short", List(String(), 1), &ast.List{}, false},
		{"list bad item", List(CIDR(), 0), &ast.List{Items: []ast.Value{str("10.0.0.0/8"), str("nope")}}, false},
		{"list rejects scalar", List(String(), 0), str("a"), false},
		{"oneof first", OneOf(IntAtLeast(1), Enum("auto")), &ast.Int{Value: 2, Source: "2"}, true},
		{"oneof second", OneOf(IntAtLeast(1), Enum("auto")), &ast.Ident{Name: "auto"}, true},
		{"oneof neither", OneOf(IntAtLeast(1), Enum("auto")), str("x"), false},
		{"nullable null", Nullable(Path()), &ast.Null{}, true},
		{"nullable inner", Nullable(Path()), str("/x"), true},
		{"nullable bad inner", Nullable(Path()), &ast.Int{Value: 1, Source: "1"}, false},
		{"operator ok", Operator("=", "~"), &ast.Operator{Op: "~"}, true},
		{"operator not allowed", Operator("=", "~"), &ast.Operator{Op: "!~"}, false},
		{"operator rejects string", Operator("="), str("="), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.typ.Check(tt.val)
			if (p == nil) != tt.ok {
				t.Errorf("Check = %+v, want ok=%v", p, tt.ok)
			}
			if p != nil && p.Msg == "" {
				t.Error("a problem must have a message")
			}
		})
	}
}

func TestListProblemPointsAtTheItem(t *testing.T) {
	bad := str("nope")
	bad.Pos.Line, bad.Pos.Col = 3, 14
	p := List(CIDR(), 0).Check(&ast.List{Items: []ast.Value{str("10.0.0.0/8"), bad}})
	if p == nil || p.Pos.Line != 3 || p.Pos.Col != 14 {
		t.Errorf("problem = %+v, want position 3:14", p)
	}
}

func TestDescribeHidesSecrets(t *testing.T) {
	s := str("hunter2")
	s.Secret = true
	if d := Describe(s); strings.Contains(d, "hunter2") {
		t.Errorf("Describe leaked a secret: %q", d)
	}
	// And type errors that quote the value hide it too.
	if p := Int(1, 2).Check(s); p == nil || strings.Contains(p.Msg, "hunter2") {
		t.Errorf("problem = %+v", p)
	}
	if p := Enum("a").Check(&ast.List{}); p == nil || !strings.Contains(p.Msg, "list") {
		t.Errorf("problem = %+v", p)
	}
}

func TestSuggest(t *testing.T) {
	cands := []string{"root", "index", "aliases", "listeners"}
	tests := map[string]string{
		"rot": `"root"`, "indx": `"index"`, "alias": `"aliases"`,
		"zzz": "", "r": "", // too far, or too short to compare
	}
	for word, want := range tests {
		if got := Suggest(word, cands); got != want {
			t.Errorf("Suggest(%q) = %q, want %q", word, got, want)
		}
	}
}

func TestParseRoute(t *testing.T) {
	op := func(s string) ast.Value { return &ast.Operator{Op: s} }
	tests := []struct {
		args []ast.Value
		kind RouteKind
		path string
		ok   bool
	}{
		{[]ast.Value{str("/a")}, RoutePrefix, "/a", true},
		{[]ast.Value{op("="), str("/a")}, RouteExact, "/a", true},
		{[]ast.Value{op("^~"), str("/a")}, RouteFinalPrefix, "/a", true},
		{[]ast.Value{op("~"), str(`\.php$`)}, RouteRegex, `\.php$`, true},
		{[]ast.Value{op("~*"), str(`\.PNG$`)}, RouteRegexFold, `\.PNG$`, true},
		{[]ast.Value{str("@fallback")}, RouteNamed, "@fallback", true},
		{[]ast.Value{op("!~"), str("/a")}, 0, "", false},
		{[]ast.Value{}, 0, "", false},
		{[]ast.Value{&ast.Int{Value: 1}}, 0, "", false},
	}
	for i, tt := range tests {
		r, ok := ParseRoute(&ast.Directive{Name: "route", Args: tt.args})
		if ok != tt.ok || (ok && (r.Kind != tt.kind || r.Path != tt.path)) {
			t.Errorf("case %d: got %+v ok=%v, want kind=%v path=%q ok=%v", i, r, ok, tt.kind, tt.path, tt.ok)
		}
	}
}

func TestUsage(t *testing.T) {
	tests := map[string]string{
		"site":             `site <name> { ... }`,
		"max_request_body": `max_request_body <size>`,
		"route":            `route [<operator>] <path> { ... }`,
		"return":           `return <status> [<target>]`,
	}
	for name, want := range tests {
		var d *Directive
		Default().All(func(_ Context, x *Directive) {
			if x.Name == name && d == nil {
				d = x
			}
		})
		if d == nil {
			t.Fatalf("no directive %q", name)
		}
		if got := d.Usage(); got != want {
			t.Errorf("Usage(%s) = %q, want %q", name, got, want)
		}
	}
}
