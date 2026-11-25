// Package loader reads a configuration file and everything it includes, and
// resolves load-time constructs: include, let, $variables, ${interpolation},
// env() and file(). The result is a single tree that contains only directives,
// with every value fully resolved. Rules: docs/configuration/grammar.md §5-6.
package loader

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/laikait/lotus/internal/config/ast"
	"github.com/laikait/lotus/internal/config/diag"
	"github.com/laikait/lotus/internal/config/parser"
	"github.com/laikait/lotus/internal/config/schema"
	"github.com/laikait/lotus/pkg/version"
)

// Limits from grammar §6.5 and §5.
const (
	DefaultMaxIncludeDepth = 16
	DefaultMaxFiles        = 1024
	maxFileFunctionSize    = 64 << 10
)

// Options configures Load. The zero value uses the real file system and environment.
type Options struct {
	FS     FS
	Getenv func(name string) (string, bool)
	// InsecureIncludes skips the ownership and permission checks on included
	// files. It is meant for development only.
	InsecureIncludes bool
	MaxIncludeDepth  int
	MaxFiles         int
}

// Result is a loaded configuration.
type Result struct {
	// File holds the root file's statements with includes spliced in and all
	// values resolved. The lotus_config_version line is removed.
	File *ast.File
	// Version is the lotus_config_version declared by the root file.
	Version int
	// Files lists every file that was read, root first.
	Files []string
}

type loader struct {
	opts    Options
	fs      FS
	diags   *diag.List
	files   []string
	stack   []string // canonical paths being processed, for cycle detection
	tooMany bool
}

type varDef struct {
	val ast.Value
	pos diag.Pos
}

type scope struct {
	vars   map[string]varDef
	parent *scope
}

func (s *scope) lookup(name string) (varDef, bool) {
	for ; s != nil; s = s.parent {
		if d, ok := s.vars[name]; ok {
			return d, true
		}
	}
	return varDef{}, false
}

func (s *scope) names() []string {
	var out []string
	for ; s != nil; s = s.parent {
		for n := range s.vars {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// fileCtx is the state while processing one file.
type fileCtx struct {
	path  string
	dir   string
	scope *scope
	depth int
}

// Load reads path and its includes. The returned Result is nil if any error
// was found. Diagnostics from every file are returned together.
func Load(path string, opts Options) (*Result, []diag.Diagnostic) {
	if opts.FS == nil {
		opts.FS = OS{}
	}
	if opts.Getenv == nil {
		opts.Getenv = os.LookupEnv
	}
	if opts.MaxIncludeDepth == 0 {
		opts.MaxIncludeDepth = DefaultMaxIncludeDepth
	}
	if opts.MaxFiles == 0 {
		opts.MaxFiles = DefaultMaxFiles
	}
	l := &loader{opts: opts, fs: opts.FS, diags: diag.NewList(diag.DefaultMaxErrors)}

	root := diag.Pos{File: path}
	src, err := l.fs.ReadFile(path)
	if err != nil {
		l.diags.Error(root, diag.CodeIncludeNotFound, "cannot read configuration file: "+errText(err))
		return nil, l.diags.Items()
	}
	canon, err := l.fs.Canonical(path)
	if err != nil {
		canon = path
	}
	l.files = append(l.files, path)
	l.stack = append(l.stack, canon)

	file := l.parse(path, src)
	c := &fileCtx{path: path, dir: filepath.Dir(path), scope: &scope{vars: map[string]varDef{}}}

	stmts := file.Statements
	var ver int
	if first, ok := firstDirective(stmts, "lotus_config_version"); ok {
		ver = l.checkVersion(first)
		stmts = stmts[1:]
	} else if !declaresVersion(stmts) {
		// (A version line that is present but not first is reported once, as
		// misplaced, while the statements are processed.)
		pos := diag.Pos{File: path, Line: 1, Col: 1}
		if len(stmts) > 0 {
			pos = stmts[0].Position()
		}
		l.diags.ErrorHint(pos, diag.CodeMissingVersion,
			"the configuration must start with lotus_config_version",
			fmt.Sprintf("add `lotus_config_version %d` as the first line", version.ConfigLanguageVersion))
	}
	out := l.statements(stmts, c)

	if l.diags.HasErrors() {
		return nil, l.diags.Items()
	}
	return &Result{File: &ast.File{Path: path, Statements: out}, Version: ver, Files: l.files}, l.diags.Items()
}

func errText(err error) string {
	if os.IsNotExist(err) {
		return "no such file"
	}
	if os.IsPermission(err) {
		return "permission denied"
	}
	return err.Error()
}

func declaresVersion(stmts []ast.Statement) bool {
	for _, s := range stmts {
		if d, ok := s.(*ast.Directive); ok && d.Name == "lotus_config_version" {
			return true
		}
	}
	return false
}

func firstDirective(stmts []ast.Statement, name string) (*ast.Directive, bool) {
	if len(stmts) == 0 {
		return nil, false
	}
	d, ok := stmts[0].(*ast.Directive)
	if !ok || d.Name != name {
		return nil, false
	}
	return d, true
}

func (l *loader) parse(path string, src []byte) *ast.File {
	if len(src) > MaxFileSize {
		l.diags.Error(diag.Pos{File: path, Line: 1, Col: 1}, diag.CodeFileTooLarge,
			fmt.Sprintf("file is larger than the %d byte limit", MaxFileSize))
		return &ast.File{Path: path}
	}
	file, ds := parser.Parse(path, src)
	for _, d := range ds {
		if d.Code == diag.CodeTooManyErrors {
			continue // the loader keeps its own overall limit
		}
		l.diags.Add(d)
	}
	return file
}

func (l *loader) checkVersion(d *ast.Directive) int {
	n, ok := singleInt(d)
	if !ok {
		l.diags.ErrorHint(d.Pos, diag.CodeMissingVersion, "lotus_config_version needs one integer",
			fmt.Sprintf("write `lotus_config_version %d`", version.ConfigLanguageVersion))
		return 0
	}
	cur := int64(version.ConfigLanguageVersion)
	switch {
	case n > cur:
		l.diags.ErrorHint(d.Args[0].Position(), diag.CodeUnsupportedVersion,
			fmt.Sprintf("configuration language version %d is newer than this build supports (%d)", n, cur),
			"upgrade lotus, or write the file for the older version")
	case n < max(1, cur-1):
		l.diags.ErrorHint(d.Args[0].Position(), diag.CodeUnsupportedVersion,
			fmt.Sprintf("configuration language version %d is no longer supported", n),
			"run `lotus config migrate` to upgrade the file")
	}
	return int(n)
}

func singleInt(d *ast.Directive) (int64, bool) {
	if len(d.Args) != 1 || d.Block != nil {
		return 0, false
	}
	n, ok := d.Args[0].(*ast.Int)
	if !ok {
		return 0, false
	}
	return n.Value, true
}

// ---- statements ----

func (l *loader) statements(stmts []ast.Statement, c *fileCtx) []ast.Statement {
	var out []ast.Statement
	for _, st := range stmts {
		if l.diags.Full() {
			return out
		}
		switch s := st.(type) {
		case *ast.Let:
			l.define(s, c)
		case *ast.Include:
			out = append(out, l.include(s, c)...)
		case *ast.Directive:
			if s.Name == "lotus_config_version" {
				l.diags.ErrorHint(s.Pos, diag.CodeVersionMisplaced,
					"lotus_config_version must appear once, as the first line of the root file",
					"remove it from this file; included files use the root file's version")
				continue
			}
			if d := l.directive(s, c); d != nil {
				out = append(out, d)
			}
		case *ast.Pair:
			key, ok1 := l.value(s.Key, c)
			val, ok2 := l.value(s.Value, c)
			if ok1 && ok2 {
				k, _ := key.(*ast.String)
				if k == nil {
					continue
				}
				out = append(out, &ast.Pair{StmtMeta: s.StmtMeta, Key: k, Value: val})
			}
		}
	}
	return out
}

func (l *loader) directive(s *ast.Directive, c *fileCtx) *ast.Directive {
	d := &ast.Directive{StmtMeta: s.StmtMeta, Pos: s.Pos, Name: s.Name}
	ok := true
	for _, a := range s.Args {
		v, good := l.value(a, c)
		if !good {
			ok = false
			continue
		}
		d.Args = append(d.Args, v)
	}
	if !ok {
		return nil
	}
	if s.Block != nil {
		d.Block = &ast.Block{
			Open: s.Block.Open, Close: s.Block.Close, Dangling: s.Block.Dangling,
			Statements: l.statements(s.Block.Statements, c),
		}
	}
	return d
}

func (l *loader) define(s *ast.Let, c *fileCtx) {
	if prev, exists := c.scope.lookup(s.Name); exists {
		l.diags.ErrorHint(s.NamePos, diag.CodeVariableRedefined,
			fmt.Sprintf("variable $%s is already defined", s.Name),
			fmt.Sprintf("first defined at %s; variables can't be redefined", prev.pos))
		return
	}
	v, ok := l.value(s.Value, c)
	if !ok {
		return
	}
	c.scope.vars[s.Name] = varDef{val: v, pos: s.NamePos}
}

// ---- include ----

func (l *loader) include(s *ast.Include, c *fileCtx) []ast.Statement {
	pv, ok := l.value(s.Path, c)
	if !ok {
		return nil
	}
	pattern, _ := pv.(*ast.String).Plain()
	secret := pv.(*ast.String).Secret
	pos := s.Path.Pos
	if pattern == "" {
		l.diags.Error(pos, diag.CodeBadIncludePath, "include path is empty")
		return nil
	}
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(c.dir, pattern)
	}
	pattern = filepath.Clean(pattern)

	const meta = "*?["
	if strings.ContainsAny(filepath.Dir(pattern), meta) {
		l.diags.ErrorHint(pos, diag.CodeBadIncludePath,
			"wildcards are only allowed in the file name, not in directory names",
			`for example "sites.d/*.conf" is fine, "*/sites.conf" is not`)
		return nil
	}
	paths := []string{pattern}
	isGlob := strings.ContainsAny(filepath.Base(pattern), meta)
	if isGlob {
		m, err := l.fs.Glob(pattern)
		if err != nil {
			l.diags.Error(pos, diag.CodeBadIncludePath, "invalid include pattern: "+err.Error())
			return nil
		}
		sort.Strings(m)
		paths = m
	}
	var out []ast.Statement
	for _, p := range paths {
		out = append(out, l.includeFile(p, pos, c, secret)...)
	}
	return out
}

// includeFile reads and processes one included file. If secret is set, the path
// came from env() or file() and is never shown in messages, file lists or positions.
func (l *loader) includeFile(path string, pos diag.Pos, parent *fileCtx, secret bool) []ast.Statement {
	name := path
	if secret {
		name = "(secret path)"
	}
	depth := parent.depth + 1
	if depth > l.opts.MaxIncludeDepth {
		l.diags.Error(pos, diag.CodeIncludeDepth,
			fmt.Sprintf("includes are nested more than %d levels deep", l.opts.MaxIncludeDepth))
		return nil
	}
	if len(l.files) >= l.opts.MaxFiles {
		if !l.tooMany {
			l.tooMany = true
			l.diags.Error(pos, diag.CodeTooManyFiles,
				fmt.Sprintf("more than %d files were included", l.opts.MaxFiles))
		}
		return nil
	}
	canon, err := l.fs.Canonical(path)
	if err != nil {
		canon = path
	}
	for i, p := range l.stack {
		if p == canon {
			chain := append(append([]string{}, l.stack[i:]...), canon)
			l.diags.ErrorHint(pos, diag.CodeIncludeCycle,
				"include cycle: "+filepath.Base(name)+" is already being processed",
				chainHint(chain, secret))
			return nil
		}
	}
	src, err := l.fs.ReadFile(path)
	if err != nil {
		l.diags.Error(pos, diag.CodeIncludeNotFound, fmt.Sprintf("cannot read included file %s: %s", name, errText(err)))
		return nil
	}
	if !l.opts.InsecureIncludes {
		if err := l.fs.CheckSecure(path); err != nil {
			l.diags.ErrorHint(pos, diag.CodeInsecureInclude,
				"refusing to include an unsafe file: "+shownErr(err, secret),
				"fix the permissions, or use --insecure-includes for development only")
			return nil
		}
	}

	l.files = append(l.files, name)
	l.stack = append(l.stack, canon)
	defer func() { l.stack = l.stack[:len(l.stack)-1] }()

	file := l.parse(name, src)
	c := &fileCtx{
		path: path, dir: filepath.Dir(path), depth: depth,
		scope: &scope{vars: map[string]varDef{}, parent: parent.scope},
	}
	return l.statements(file.Statements, c)
}

// ---- values ----

func (l *loader) value(v ast.Value, c *fileCtx) (ast.Value, bool) {
	switch v := v.(type) {
	case *ast.String:
		return l.str(v, c)
	case *ast.Variable:
		def, ok := c.scope.lookup(v.Name)
		if !ok {
			l.undefined(v.Pos, v.Name, c)
			return nil, false
		}
		return cloneAt(def.val, v.Pos), true
	case *ast.Call:
		return l.call(v, c)
	case *ast.List:
		out := &ast.List{Pos: v.Pos}
		ok := true
		for _, item := range v.Items {
			r, good := l.value(item, c)
			if !good {
				ok = false
				continue
			}
			out.Items = append(out.Items, r)
		}
		return out, ok
	default:
		return v, true
	}
}

func (l *loader) undefined(pos diag.Pos, name string, c *fileCtx) {
	hint := fmt.Sprintf("define it first with: let $%s = ...", name)
	if s := schema.Suggest(name, c.scope.names()); s != "" {
		hint = "did you mean $" + strings.Trim(s, `"`) + "?"
	}
	l.diags.ErrorHint(pos, diag.CodeUndefinedVariable, fmt.Sprintf("variable $%s is not defined", name), hint)
}

// str resolves ${name} parts in a string.
func (l *loader) str(s *ast.String, c *fileCtx) (ast.Value, bool) {
	hasVar := false
	for _, p := range s.Parts {
		if p.Kind == ast.PartVar {
			hasVar = true
			break
		}
	}
	if !hasVar {
		return s, true
	}
	out := &ast.String{Pos: s.Pos, Source: s.Source, Secret: s.Secret}
	ok := true
	for _, p := range s.Parts {
		if p.Kind != ast.PartVar {
			out.Parts = append(out.Parts, p)
			continue
		}
		def, found := c.scope.lookup(p.Var)
		if !found {
			l.undefined(s.Pos, p.Var, c)
			ok = false
			continue
		}
		switch v := def.val.(type) {
		case *ast.String:
			out.Parts = append(out.Parts, v.Parts...)
			out.Secret = out.Secret || v.Secret
		case *ast.Int:
			out.Parts = append(out.Parts, ast.StringPart{Text: v.Source})
		case *ast.Size:
			out.Parts = append(out.Parts, ast.StringPart{Text: v.Source})
		case *ast.Duration:
			out.Parts = append(out.Parts, ast.StringPart{Text: v.Source})
		case *ast.Bool:
			out.Parts = append(out.Parts, ast.StringPart{Text: fmt.Sprint(v.Value)})
		case *ast.Ident:
			out.Parts = append(out.Parts, ast.StringPart{Text: v.Name})
		default:
			l.diags.ErrorHint(s.Pos, diag.CodeVariableType,
				fmt.Sprintf("variable $%s can't be used inside a string", p.Var),
				fmt.Sprintf("it was defined at %s and holds %s; use it as a whole value instead", def.pos, kindName(def.val)))
			ok = false
		}
	}
	return out, ok
}

func kindName(v ast.Value) string {
	switch v.(type) {
	case *ast.List:
		return "a list"
	case *ast.Null:
		return "null"
	default:
		return "a value of another type"
	}
}

func (l *loader) call(call *ast.Call, c *fileCtx) (ast.Value, bool) {
	switch call.Name {
	case "env":
		return l.callEnv(call, c)
	case "file":
		return l.callFile(call, c)
	}
	hint := "available functions: env, file"
	if s := schema.Suggest(call.Name, []string{"env", "file"}); s != "" {
		hint = "did you mean " + s + "?"
	}
	l.diags.ErrorHint(call.Pos, diag.CodeBadFunction, fmt.Sprintf("unknown function %s()", call.Name), hint)
	return nil, false
}

// stringArgs resolves function arguments, which must be strings and not calls.
// secret reports whether any argument came from env() or file().
func (l *loader) stringArgs(call *ast.Call, min, max int, c *fileCtx) (args []string, secret, ok bool) {
	if len(call.Args) < min || len(call.Args) > max {
		l.diags.Error(call.Pos, diag.CodeBadFunction,
			fmt.Sprintf("%s() takes %d to %d arguments, got %d", call.Name, min, max, len(call.Args)))
		return nil, false, false
	}
	for _, a := range call.Args {
		if _, nested := a.(*ast.Call); nested {
			l.diags.Error(a.Position(), diag.CodeBadFunction, "functions can't be nested")
			return nil, false, false
		}
		v, good := l.value(a, c)
		if !good {
			return nil, false, false
		}
		s, isStr := v.(*ast.String)
		if !isStr {
			l.diags.Error(a.Position(), diag.CodeBadFunction, call.Name+"() arguments must be strings")
			return nil, false, false
		}
		text, _ := s.Plain()
		args = append(args, text)
		secret = secret || s.Secret
	}
	return args, secret, true
}

func secretString(pos diag.Pos, text string) *ast.String {
	return &ast.String{
		Pos:    pos,
		Parts:  []ast.StringPart{{Kind: ast.PartText, Text: text, Escaped: true}},
		Secret: true,
	}
}

func (l *loader) callEnv(call *ast.Call, c *fileCtx) (ast.Value, bool) {
	args, secret, ok := l.stringArgs(call, 1, 2, c)
	if !ok {
		return nil, false
	}
	if val, set := l.opts.Getenv(args[0]); set {
		return secretString(call.Pos, val), true
	}
	if len(args) == 2 {
		return secretString(call.Pos, args[1]), true
	}
	if secret { // the variable's name itself came from env() or file()
		l.diags.Error(call.Pos, diag.CodeEnvNotSet, "an environment variable (named by a secret value) is not set")
		return nil, false
	}
	l.diags.ErrorHint(call.Pos, diag.CodeEnvNotSet,
		fmt.Sprintf("environment variable %s is not set", args[0]),
		`set it, or give a default: env("`+args[0]+`", "default")`)
	return nil, false
}

func (l *loader) callFile(call *ast.Call, c *fileCtx) (ast.Value, bool) {
	args, secret, ok := l.stringArgs(call, 1, 1, c)
	if !ok {
		return nil, false
	}
	path := args[0]
	if !filepath.IsAbs(path) {
		path = filepath.Join(c.dir, path)
	}
	name := path
	if secret {
		name = "(secret path)"
	}
	data, err := l.fs.ReadFile(path)
	if err != nil {
		l.diags.Error(call.Pos, diag.CodeFileFunction, fmt.Sprintf("file(): cannot read %s: %s", name, errText(err)))
		return nil, false
	}
	if len(data) > maxFileFunctionSize {
		l.diags.Error(call.Pos, diag.CodeFileFunction,
			fmt.Sprintf("file(): %s is larger than %d bytes", name, maxFileFunctionSize))
		return nil, false
	}
	if !utf8.Valid(data) {
		l.diags.Error(call.Pos, diag.CodeFileFunction, fmt.Sprintf("file(): %s is not valid UTF-8", name))
		return nil, false
	}
	text := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	return secretString(call.Pos, text), true
}

// cloneAt returns a shallow copy of v positioned at pos, so diagnostics about
// a use of a variable point at the use, not the definition.
func cloneAt(v ast.Value, pos diag.Pos) ast.Value {
	switch v := v.(type) {
	case *ast.String:
		c := *v
		c.Pos = pos
		return &c
	case *ast.Int:
		c := *v
		c.Pos = pos
		return &c
	case *ast.Size:
		c := *v
		c.Pos = pos
		return &c
	case *ast.Duration:
		c := *v
		c.Pos = pos
		return &c
	case *ast.Bool:
		c := *v
		c.Pos = pos
		return &c
	case *ast.Null:
		return &ast.Null{Pos: pos}
	case *ast.Ident:
		c := *v
		c.Pos = pos
		return &c
	case *ast.List:
		c := &ast.List{Pos: pos, Items: make([]ast.Value, len(v.Items))}
		for i, item := range v.Items {
			c.Items[i] = cloneAt(item, item.Position())
		}
		return c
	default:
		return v
	}
}

func chainHint(chain []string, secret bool) string {
	if secret {
		return ""
	}
	return "chain: " + strings.Join(chain, " -> ")
}

func shownErr(err error, secret bool) string {
	if secret {
		return "(details hidden: the path comes from env() or file())"
	}
	return err.Error()
}
