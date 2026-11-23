// Package validator checks a loaded configuration tree against the directive
// registry: names, contexts, cardinality, argument types, placeholders and
// cross-references. Run it on the output of the loader.
package validator

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/laikait/lotus/internal/config/ast"
	"github.com/laikait/lotus/internal/config/diag"
	"github.com/laikait/lotus/internal/config/schema"
)

// Validate checks f against reg and returns all problems found (up to the
// error limit). f must come from the loader, so includes, variables and
// functions are already resolved.
func Validate(f *ast.File, reg *schema.Registry) []diag.Diagnostic {
	v := &validator{
		reg:        reg,
		diags:      diag.NewList(diag.DefaultMaxErrors),
		defs:       map[string]map[string]diag.Pos{},
		secretDefs: map[string]bool{},
		nsDir:      map[string]string{},
	}
	reg.All(func(_ schema.Context, d *schema.Directive) {
		if d.Defines != "" {
			v.nsDir[d.Defines] = d.Name
		}
	})
	v.root = diag.Pos{File: f.Path, Line: 1, Col: 1}
	v.block(schema.CtxRoot, f.Statements, nil)
	v.resolveRefs()
	return v.diags.Items()
}

type reference struct {
	ns, name string
	pos      diag.Pos
	secret   bool
}

// label quotes a value for a message, or hides it if it is secret.
func label(s string, secret bool) string {
	if secret {
		return "(secret value)"
	}
	return strconv.Quote(s)
}

// secretValue reports whether v is, or contains, a secret string.
func secretValue(v ast.Value) bool {
	switch x := v.(type) {
	case *ast.String:
		return x.Secret
	case *ast.List:
		for _, item := range x.Items {
			if secretValue(item) {
				return true
			}
		}
	}
	return false
}

type validator struct {
	reg   *schema.Registry
	diags *diag.List
	root  diag.Pos
	defs  map[string]map[string]diag.Pos // namespace -> name -> definition
	// secretDefs marks definitions whose name is secret (namespace\x00name).
	secretDefs map[string]bool
	nsDir      map[string]string // namespace -> directive that defines it
	refs       []reference
	siteFile   string // file of the enclosing site, "" outside a site
}

// Error implements schema.Reporter.
func (v *validator) Error(pos diag.Pos, code, msg, hint string) {
	v.diags.ErrorHint(pos, code, msg, hint)
}

func (v *validator) block(ctx schema.Context, stmts []ast.Statement, owner *ast.Directive) {
	cs := v.reg.Context(ctx)
	if cs == nil {
		v.Error(v.root, diag.CodeUnknownDirective, fmt.Sprintf("internal error: no schema for context %q", ctx), "")
		return
	}
	single := map[string]diag.Pos{}
	keyed := map[string]diag.Pos{}
	pairKeys := map[string]diag.Pos{}
	present := map[string]bool{}

	for _, st := range stmts {
		if v.diags.Full() {
			return
		}
		switch s := st.(type) {
		case *ast.Directive:
			present[s.Name] = true
			v.directive(cs, s, single, keyed)
		case *ast.Pair:
			v.pair(cs, s, pairKeys)
		default:
			v.Error(st.Position(), diag.CodeUnexpectedToken, "internal error: unresolved statement in validated tree", "")
		}
	}

	for _, name := range cs.Required {
		if !present[name] {
			pos, what := v.root, "the configuration"
			if owner != nil {
				pos, what = owner.Pos, fmt.Sprintf("the %q block", owner.Name)
			}
			v.Error(pos, diag.CodeMissingRequired,
				fmt.Sprintf("%s needs a %q directive", what, name),
				fmt.Sprintf("usage: %s", v.reg.Lookup(ctx, name).Usage()))
		}
	}
}

func where(ctx schema.Context) string {
	if ctx == schema.CtxRoot {
		return "at the top level"
	}
	return fmt.Sprintf("inside %q", string(ctx))
}

func (v *validator) unknown(cs *schema.ContextSpec, d *ast.Directive) {
	if cs.Pairs != nil {
		v.Error(d.Pos, diag.CodeWrongContext,
			fmt.Sprintf("%q is not allowed %s", d.Name, where(cs.Name)),
			`this block only accepts "Name" = "value" entries`)
		return
	}
	if others := v.reg.ContextsDefining(d.Name); len(others) > 0 {
		names := make([]string, len(others))
		for i, o := range others {
			if o == schema.CtxRoot {
				names[i] = "the top level"
			} else {
				names[i] = fmt.Sprintf("%q", string(o))
			}
		}
		v.Error(d.Pos, diag.CodeWrongContext,
			fmt.Sprintf("%q is not allowed %s", d.Name, where(cs.Name)),
			"it belongs in: "+strings.Join(names, ", "))
		return
	}
	hint := "directives allowed here: " + strings.Join(cs.Names(), ", ")
	if len(cs.Names()) == 0 {
		hint = ""
	}
	if s := schema.Suggest(d.Name, cs.Names()); s != "" {
		hint = "did you mean " + s + "?"
	}
	v.Error(d.Pos, diag.CodeUnknownDirective, fmt.Sprintf("unknown directive %q", d.Name), hint)
}

func (v *validator) directive(cs *schema.ContextSpec, d *ast.Directive, single, keyed map[string]diag.Pos) {
	spec := cs.Directives[d.Name]
	if spec == nil {
		v.unknown(cs, d)
		return
	}

	if v.siteFile != "" && spec.TenantForbidden && d.Pos.File != v.siteFile {
		v.Error(d.Pos, diag.CodeTenantBoundary,
			fmt.Sprintf("%q can't be set in a file included from a site", d.Name),
			fmt.Sprintf("this file is %s but the site is defined in %s; put it in the site's own file", d.Pos.File, v.siteFile))
	}

	switch spec.Card {
	case schema.Single:
		if first, dup := single[d.Name]; dup {
			v.Error(d.Pos, diag.CodeDuplicateDirective,
				fmt.Sprintf("%q is given more than once", d.Name),
				fmt.Sprintf("first set at %s; it may appear only once here", first))
		} else {
			single[d.Name] = d.Pos
		}
	case schema.Keyed:
		if len(d.Args) > 0 {
			if key, ok := plain(d.Args[0]); ok {
				k := d.Name + "\x00" + key
				if first, dup := keyed[k]; dup {
					v.Error(d.Args[0].Position(), diag.CodeDuplicateKey,
						fmt.Sprintf("%s %s is defined more than once", d.Name, label(key, secretValue(d.Args[0]))),
						fmt.Sprintf("first defined at %s", first))
				} else {
					keyed[k] = d.Args[0].Position()
					if spec.Defines != "" {
						v.define(spec, key, d.Args[0].Position(), secretValue(d.Args[0]))
					}
				}
			}
		}
	}

	v.args(spec, d)

	switch {
	case spec.Block == schema.BlockNone && d.Block != nil:
		v.Error(d.Block.Open, diag.CodeBlockMismatch,
			fmt.Sprintf("%q does not take a block", d.Name), "usage: "+spec.Usage())
	case spec.Block == schema.BlockRequired && d.Block == nil:
		v.Error(d.Pos, diag.CodeBlockMismatch,
			fmt.Sprintf("%q needs a block", d.Name), "usage: "+spec.Usage())
	}

	if d.Block != nil && spec.Block != schema.BlockNone {
		prev := v.siteFile
		if spec.Opens == schema.CtxSite {
			v.siteFile = d.Pos.File
		}
		v.block(spec.Opens, d.Block.Statements, d)
		v.siteFile = prev
	}

	if spec.Validate != nil {
		spec.Validate(d, v)
	}
}

func (v *validator) define(spec *schema.Directive, name string, pos diag.Pos, secret bool) {
	ns := spec.Defines
	if v.defs[ns] == nil {
		v.defs[ns] = map[string]diag.Pos{}
	}
	if _, exists := v.defs[ns][name]; !exists {
		v.defs[ns][name] = pos
		if secret {
			v.secretDefs[ns+"\x00"+name] = true
		}
	}
}

// args matches arguments to the directive's argument specs. An optional
// argument is skipped when there are only enough values left for the
// required arguments after it.
func (v *validator) args(spec *schema.Directive, d *ast.Directive) {
	args := d.Args
	i := 0
	for si, a := range spec.Args {
		remaining := len(args) - i
		if a.Variadic {
			for ; i < len(args); i++ {
				v.checkArg(spec, a, args[i])
			}
			continue
		}
		if a.Optional && remaining <= requiredAfter(spec.Args[si+1:]) {
			continue
		}
		if remaining == 0 {
			name := a.Name
			if name == "" {
				name = a.Type.Name()
			}
			v.Error(d.Pos, diag.CodeArgCount,
				fmt.Sprintf("%q is missing its <%s> argument", d.Name, name), "usage: "+spec.Usage())
			return
		}
		if spec.Secret {
			markSecret(args[i])
		}
		v.checkArg(spec, a, args[i])
		i++
	}
	if i < len(args) {
		v.Error(args[i].Position(), diag.CodeArgCount,
			fmt.Sprintf("%q has too many arguments", d.Name), "usage: "+spec.Usage())
	}
}

func requiredAfter(specs []schema.Arg) int {
	n := 0
	for _, a := range specs {
		if !a.Optional && !a.Variadic {
			n++
		}
	}
	return n
}

func (v *validator) checkArg(spec *schema.Directive, a schema.Arg, val ast.Value) {
	if p := a.Type.Check(val); p != nil {
		pos := p.Pos
		if !pos.IsValid() {
			pos = val.Position()
		}
		name := a.Name
		if name == "" {
			name = a.Type.Name()
		}
		if secretValue(val) {
			// Type messages quote the offending value, so they are replaced
			// entirely. Secrets must never reach diagnostics.
			v.Error(val.Position(), diag.CodeInvalidValue,
				fmt.Sprintf("invalid %s in %q: expected %s (the value is hidden because it comes from env() or file())", name, spec.Name, a.Type.Name()), "")
			return
		}
		v.Error(pos, diag.CodeInvalidValue, fmt.Sprintf("invalid %s in %q: %s", name, spec.Name, p.Msg), p.Hint)
		return
	}
	if a.Ref != "" {
		v.collectRefs(a.Ref, val)
	}
	if !a.Raw {
		v.placeholders(spec.Name, spec.Placeholders, val)
	}
}

func (v *validator) collectRefs(ns string, val ast.Value) {
	switch x := val.(type) {
	case *ast.String:
		if s, ok := x.Plain(); ok {
			v.refs = append(v.refs, reference{ns: ns, name: s, pos: x.Pos, secret: x.Secret})
		}
	case *ast.List:
		for _, item := range x.Items {
			v.collectRefs(ns, item)
		}
	}
}

func (v *validator) resolveRefs() {
	for _, r := range v.refs {
		if _, ok := v.defs[r.ns][r.name]; ok {
			continue
		}
		dir := v.nsDir[r.ns]
		if dir == "" {
			dir = r.ns
		}
		if r.secret {
			v.Error(r.pos, diag.CodeUnknownReference,
				fmt.Sprintf("%s %s is not defined", r.ns, label("", true)),
				fmt.Sprintf("the name is hidden because it comes from env() or file(); define it with a %q directive", dir))
			continue
		}
		var known []string
		for n := range v.defs[r.ns] {
			if !v.secretDefs[r.ns+"\x00"+n] { // never reveal secret names in hints
				known = append(known, n)
			}
		}
		sort.Strings(known)
		hint := fmt.Sprintf("define it with: %s %q { ... }", dir, r.name)
		if s := schema.Suggest(r.name, known); s != "" {
			hint = "did you mean " + s + "?"
		} else if len(known) > 0 {
			hint = "defined: " + strings.Join(known, ", ")
		}
		v.Error(r.pos, diag.CodeUnknownReference, fmt.Sprintf("%s %q is not defined", r.ns, r.name), hint)
	}
}

func (v *validator) pair(cs *schema.ContextSpec, s *ast.Pair, seen map[string]diag.Pos) {
	if cs.Pairs == nil {
		v.Error(s.Key.Pos, diag.CodeWrongContext,
			fmt.Sprintf(`"key" = value entries are not allowed %s`, where(cs.Name)), "")
		return
	}
	ps := cs.Pairs
	hidden := secretValue(s.Key) || secretValue(s.Value)
	const hiddenMsg = "invalid map entry (the value is hidden because it comes from env() or file())"
	if p := ps.Key.Check(s.Key); p != nil {
		if hidden {
			v.Error(s.Key.Pos, diag.CodeInvalidValue, hiddenMsg, "")
			return
		}
		v.Error(s.Key.Pos, diag.CodeInvalidValue, "invalid map key: "+p.Msg, p.Hint)
		return
	}
	if p := ps.Value.Check(s.Value); p != nil {
		pos := p.Pos
		if !pos.IsValid() {
			pos = s.Value.Position()
		}
		if hidden {
			v.Error(pos, diag.CodeInvalidValue, hiddenMsg, "")
			return
		}
		v.Error(pos, diag.CodeInvalidValue, "invalid map value: "+p.Msg, p.Hint)
		return
	}
	if ps.Validate != nil {
		if p := ps.Validate(s.Key, s.Value); p != nil {
			if hidden {
				v.Error(s.Key.Pos, diag.CodeInvalidValue, hiddenMsg, "")
				return
			}
			pos := p.Pos
			if !pos.IsValid() {
				pos = s.Key.Pos
			}
			v.Error(pos, diag.CodeInvalidValue, p.Msg, p.Hint)
			return
		}
	}
	key, _ := s.Key.Plain()
	if ps.CaseInsensitive {
		key = strings.ToLower(key)
	}
	if first, dup := seen[key]; dup {
		v.Error(s.Key.Pos, diag.CodeDuplicateKey, fmt.Sprintf("key %s is defined more than once", label(key, s.Key.Secret)),
			fmt.Sprintf("first defined at %s", first))
		return
	}
	seen[key] = s.Key.Pos
	v.placeholders(string(cs.Name), false, s.Value)
}

func plain(v ast.Value) (string, bool) {
	s, ok := v.(*ast.String)
	if !ok {
		return "", false
	}
	return s.Plain()
}

func markSecret(v ast.Value) {
	switch x := v.(type) {
	case *ast.String:
		x.Secret = true
	case *ast.List:
		for _, item := range x.Items {
			markSecret(item)
		}
	}
}
