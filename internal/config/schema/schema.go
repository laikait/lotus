package schema

import (
	"sort"
	"strings"

	"github.com/laikait/lotus/internal/config/ast"
	"github.com/laikait/lotus/internal/config/diag"
)

// Context names a place where directives can appear: the file root or the body
// of a block. A directive opens a context for its block.
type Context string

// Contexts.
const (
	CtxRoot            Context = "root"
	CtxServer          Context = "server"
	CtxRuntime         Context = "runtime"
	CtxListen          Context = "listen"
	CtxLimits          Context = "limits"
	CtxTLS             Context = "tls"
	CtxACME            Context = "acme"
	CtxDNS             Context = "dns"
	CtxMetrics         Context = "metrics"
	CtxSite            Context = "site"
	CtxRoute           Context = "route"
	CtxMatch           Context = "match"
	CtxLogs            Context = "logs"
	CtxPHPBackend      Context = "php_backend"
	CtxUpstream        Context = "upstream"
	CtxBackend         Context = "backend"
	CtxHealthCheck     Context = "health_check"
	CtxRateLimit       Context = "rate_limit"
	CtxSecurityHeaders Context = "security_headers"
	CtxControl         Context = "control"
)

// Cardinality says how often a directive may appear in one context.
type Cardinality int

// Cardinalities (grammar §6.6).
const (
	Single   Cardinality = iota // at most once
	Repeated                    // any number of times, order kept
	Keyed                       // any number of times; the first argument is a unique key
)

// BlockMode says whether a directive takes a `{ ... }` block.
type BlockMode int

// Block modes.
const (
	BlockNone BlockMode = iota
	BlockOptional
	BlockRequired
)

// Arg describes one positional argument.
type Arg struct {
	Name     string
	Type     Type
	Optional bool // may be left out (it is skipped if the value doesn't match)
	Variadic bool // takes all remaining arguments; must be last
	// Raw marks arguments that are patterns or paths matched literally. They
	// are never scanned for request-time placeholders.
	Raw bool
	// Ref names a namespace the value refers to (see Directive.Defines). Every
	// string, or every string in a list, must be defined in that namespace.
	Ref string
}

// Reporter receives problems found by a directive's Validate hook.
type Reporter interface {
	Error(pos diag.Pos, code, msg, hint string)
}

// Directive describes one directive in one context.
type Directive struct {
	Name  string
	Card  Cardinality
	Args  []Arg
	Block BlockMode
	Opens Context // the context of the block, when Block != BlockNone

	// Placeholders allows request-time placeholders like {uri} in string arguments.
	Placeholders bool
	// Defines names a namespace this keyed directive's key is added to, so other
	// directives can refer to it with Arg.Ref.
	Defines string
	// Secret marks arguments whose values must be redacted everywhere.
	Secret bool
	// TenantForbidden blocks this directive in files included from inside a
	// site (grammar §6.5), so tenant-managed files can't change it.
	TenantForbidden bool

	// Documentation (docs/configuration/directive-template.md).
	Since       string
	Default     string
	Description string
	Security    string

	// Validate runs after the directive and its block were checked. It can do
	// checks that involve several arguments or child directives.
	Validate func(d *ast.Directive, rep Reporter)
}

// PairSpec describes the `"key" = value` entries a map-like block accepts.
type PairSpec struct {
	Key, Value      Type
	CaseInsensitive bool // keys differing only in case are duplicates
	// Validate can reject an entry the type checks accepted.
	Validate func(key *ast.String, value ast.Value) *Problem
}

// ContextSpec describes one context.
type ContextSpec struct {
	Name       Context
	Directives map[string]*Directive
	// Required lists directives that must appear at least once.
	Required []string
	// Check validates a block as a whole, given the names of the directives it
	// contains. It returns a diagnostic code, a message and a hint, or an empty
	// message if all is well.
	Check func(present map[string]bool) (code, msg, hint string)
	// Pairs is set for map-like blocks, which accept only `"key" = value` entries.
	Pairs *PairSpec
}

// Registry is a complete set of contexts and directives.
type Registry struct {
	contexts map[Context]*ContextSpec
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{contexts: map[Context]*ContextSpec{}} }

// Define adds directives to a context, creating it if needed.
func (r *Registry) Define(ctx Context, ds ...*Directive) *ContextSpec {
	cs := r.contexts[ctx]
	if cs == nil {
		cs = &ContextSpec{Name: ctx, Directives: map[string]*Directive{}}
		r.contexts[ctx] = cs
	}
	for _, d := range ds {
		if _, dup := cs.Directives[d.Name]; dup {
			panic("schema: duplicate directive " + d.Name + " in context " + string(ctx))
		}
		cs.Directives[d.Name] = d
	}
	return cs
}

// Context returns the spec for a context, or nil.
func (r *Registry) Context(ctx Context) *ContextSpec { return r.contexts[ctx] }

// Lookup returns the directive spec for name in ctx, or nil.
func (r *Registry) Lookup(ctx Context, name string) *Directive {
	if cs := r.contexts[ctx]; cs != nil {
		return cs.Directives[name]
	}
	return nil
}

// ContextsDefining returns the contexts that define a directive called name, sorted.
func (r *Registry) ContextsDefining(name string) []Context {
	var out []Context
	for ctx, cs := range r.contexts {
		if _, ok := cs.Directives[name]; ok {
			out = append(out, ctx)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Names returns the sorted directive names of a context.
func (cs *ContextSpec) Names() []string {
	names := make([]string, 0, len(cs.Directives))
	for n := range cs.Directives {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// All calls fn for every directive in every context, in a stable order.
func (r *Registry) All(fn func(ctx Context, d *Directive)) {
	ctxs := make([]string, 0, len(r.contexts))
	for c := range r.contexts {
		ctxs = append(ctxs, string(c))
	}
	sort.Strings(ctxs)
	for _, c := range ctxs {
		cs := r.contexts[Context(c)]
		for _, n := range cs.Names() {
			fn(cs.Name, cs.Directives[n])
		}
	}
}

// Usage renders a one-line usage summary such as `max_request_body <size>`.
func (d *Directive) Usage() string {
	var b strings.Builder
	b.WriteString(d.Name)
	for _, a := range d.Args {
		name := a.Name
		if name == "" {
			name = a.Type.Name()
		}
		switch {
		case a.Variadic:
			b.WriteString(" <" + name + ">...")
		case a.Optional:
			b.WriteString(" [<" + name + ">]")
		default:
			b.WriteString(" <" + name + ">")
		}
	}
	switch d.Block {
	case BlockRequired:
		b.WriteString(" { ... }")
	case BlockOptional:
		b.WriteString(" [{ ... }]")
	}
	return b.String()
}
