package schema

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/laikait/lotus/internal/config/ast"
	"github.com/laikait/lotus/internal/config/diag"
)

// RouteKind is how a route matches a path (docs/architecture/routing-precedence.md §2).
type RouteKind int

// Route kinds.
const (
	RoutePrefix      RouteKind = iota // route "/p"
	RouteFinalPrefix                  // route ^~ "/p"
	RouteExact                        // route = "/p"
	RouteRegex                        // route ~ 're'
	RouteRegexFold                    // route ~* 're'
	RouteNamed                        // route "@name"
)

// ParsedRoute is the meaning of a `route` directive's arguments.
type ParsedRoute struct {
	Kind RouteKind
	Path string // path, pattern or name
	// Secret is set when the path came from env() or file(); it must not be echoed.
	Secret bool
	Pos    diag.Pos
	Op     string
}

// ParseRoute reads a route directive's arguments. ok is false if they are not
// of the expected shape (the argument checks report that separately).
func ParseRoute(d *ast.Directive) (r ParsedRoute, ok bool) {
	args := d.Args
	r.Pos = d.Pos
	if len(args) > 0 {
		if op, isOp := args[0].(*ast.Operator); isOp {
			r.Op = op.Op
			args = args[1:]
		}
	}
	if len(args) != 1 {
		return r, false
	}
	path, isStr := plainString(args[0])
	if !isStr {
		return r, false
	}
	r.Path = path
	if s, ok := args[0].(*ast.String); ok {
		r.Secret = s.Secret
	}
	r.Pos = args[0].Position()
	switch r.Op {
	case "":
		if strings.HasPrefix(path, "@") {
			r.Kind = RouteNamed
		} else {
			r.Kind = RoutePrefix
		}
	case "=":
		r.Kind = RouteExact
	case "^~":
		r.Kind = RouteFinalPrefix
	case "~":
		r.Kind = RouteRegex
	case "~*":
		r.Kind = RouteRegexFold
	default:
		return r, false
	}
	return r, true
}

var namedRouteRe = regexp.MustCompile(`^@[a-z0-9_-]+$`)

// validateRoute checks one route directive and the routes nested in it.
func validateRoute(d *ast.Directive, rep Reporter) {
	r, ok := ParseRoute(d)
	if !ok {
		return // argument count and type problems are reported by the validator
	}
	switch r.Kind {
	case RouteRegex, RouteRegexFold:
		pattern := r.Path
		if r.Kind == RouteRegexFold {
			pattern = "(?i)" + pattern
		}
		if _, err := regexp.Compile(pattern); err != nil {
			detail := strings.TrimPrefix(err.Error(), "error parsing regexp: ")
			if r.Secret {
				detail = "(details hidden: the pattern comes from env() or file())"
			}
			rep.Error(r.Pos, diag.CodeInvalidRegex,
				"invalid regular expression: "+detail,
				"LOTUS uses RE2 syntax: no backreferences or lookaround")
		}
	case RouteNamed:
		if !namedRouteRe.MatchString(r.Path) {
			rep.Error(r.Pos, diag.CodeRoutePath,
				fmt.Sprintf("named route %s is malformed", r.show()),
				"names look like @fallback: '@' followed by lowercase letters, digits, '_' or '-'")
		}
	default:
		if !strings.HasPrefix(r.Path, "/") {
			rep.Error(r.Pos, diag.CodeRoutePath,
				fmt.Sprintf("route path %s must start with '/'", r.show()),
				`write "/"+path, or use "@name" for a named route`)
		} else if strings.ContainsAny(r.Path, "?#") {
			rep.Error(r.Pos, diag.CodeRoutePath,
				fmt.Sprintf("route path %s must not contain '?' or '#'", r.show()),
				"routes match the path only; use a match block to test the query string")
		}
	}
	checkProxyConflicts(d, rep)
	checkCacheNeeds(d, rep)
	if d.Block != nil {
		checkRoutesWith(rep, d, childRoutes(d.Block))
	}
}

// checkCacheNeeds reports a cache on a route that has nothing to cache: only the
// responses of a proxy or of PHP are kept, never static files.
func checkCacheNeeds(d *ast.Directive, rep Reporter) {
	if d.Block == nil {
		return
	}
	var cache *ast.Directive
	dynamic := false
	var scan func(stmts []ast.Statement)
	scan = func(stmts []ast.Statement) {
		for _, s := range stmts {
			c, ok := s.(*ast.Directive)
			if !ok {
				continue
			}
			switch c.Name {
			case "cache":
				if cache == nil {
					cache = c
				}
			case "proxy", "php":
				dynamic = true
			case "match":
				if c.Block != nil {
					scan(c.Block.Statements)
				}
			}
		}
	}
	scan(d.Block.Statements)
	if cache != nil && !dynamic {
		rep.Error(cache.Pos, diag.CodeInvalidValue, "cache needs a route that proxies or runs PHP",
			"only responses of a backend are kept; static files are already served from the file system cache of the operating system")
	}
}

// checkProxyConflicts reports a route that both proxies and serves files: only
// one of them can answer, and which one wins would be a guess.
func checkProxyConflicts(d *ast.Directive, rep Reporter) {
	if d.Block == nil {
		return
	}
	hasProxy := false
	for _, s := range d.Block.Statements {
		if c, ok := s.(*ast.Directive); ok && c.Name == "proxy" {
			hasProxy = true
		}
	}
	if !hasProxy {
		return
	}
	for _, s := range d.Block.Statements {
		if c, ok := s.(*ast.Directive); ok && (c.Name == "try_files" || c.Name == "php") {
			rep.Error(c.Pos, diag.CodeInvalidValue, fmt.Sprintf("%s can't be combined with proxy in one route", c.Name),
				"a route either proxies or serves files; use a separate route for each")
		}
	}
}

func childRoutes(b *ast.Block) []*ast.Directive {
	var out []*ast.Directive
	for _, s := range b.Statements {
		if d, ok := s.(*ast.Directive); ok && d.Name == "route" {
			out = append(out, d)
		}
	}
	return out
}

// checkRoutesWith applies the sibling rules: no duplicates, and nesting only
// inside prefix routes. parent is nil for routes directly inside a site.
func checkRoutesWith(rep Reporter, parent *ast.Directive, children []*ast.Directive) {
	var parentRoute *ParsedRoute
	if parent != nil {
		if p, ok := ParseRoute(parent); ok {
			parentRoute = &p
		}
	}
	seen := map[string]diag.Pos{}
	for _, c := range children {
		r, ok := ParseRoute(c)
		if !ok {
			continue
		}
		if parentRoute != nil {
			switch {
			case parentRoute.Kind != RoutePrefix && parentRoute.Kind != RouteFinalPrefix:
				rep.Error(c.Pos, diag.CodeRouteNesting,
					"only prefix routes can contain other routes",
					fmt.Sprintf("the enclosing route at %s is not a prefix route", parentRoute.Pos))
				continue
			case r.Kind == RouteNamed:
				rep.Error(r.Pos, diag.CodeRouteNesting,
					"named routes must be defined directly inside a site",
					"they are only reachable by internal redirects, so they can't belong to one route")
				continue
			case (r.Kind == RoutePrefix || r.Kind == RouteFinalPrefix || r.Kind == RouteExact) && !strings.HasPrefix(r.Path, parentRoute.Path):
				rep.Error(r.Pos, diag.CodeRouteNesting,
					fmt.Sprintf("route path %s is outside its parent route %s", r.show(), parentRoute.show()),
					"a nested route's path must start with its parent's path")
				continue
			}
		}
		var key string
		switch r.Kind {
		case RoutePrefix, RouteFinalPrefix:
			key = "prefix " + r.Path
		case RouteExact:
			key = "exact " + r.Path
		case RouteRegex:
			key = "regex " + r.Path
		case RouteRegexFold:
			key = "regex-fold " + r.Path
		case RouteNamed:
			key = "named " + r.Path
		}
		if first, dup := seen[key]; dup {
			rep.Error(r.Pos, diag.CodeRouteDuplicate,
				fmt.Sprintf("duplicate route %s", r.show()),
				fmt.Sprintf("the same route is already defined at %s", first))
			continue
		}
		seen[key] = r.Pos
	}
}

// validateErrorPage checks the target of an error_page directive: a path that
// starts with "/" or a well-formed named route.
func validateErrorPage(d *ast.Directive, rep Reporter) {
	if len(d.Args) != 2 {
		return
	}
	target, ok := plainString(d.Args[1])
	if !ok {
		return
	}
	pos := d.Args[1].Position()
	secret := false
	if s, isStr := d.Args[1].(*ast.String); isStr {
		secret = s.Secret
	}
	switch {
	case strings.HasPrefix(target, "@"):
		if !namedRouteRe.MatchString(target) {
			rep.Error(pos, diag.CodeRoutePath, "the named route is malformed",
				"names look like @error: '@' followed by lowercase letters, digits, '_' or '-'")
		}
	case !strings.HasPrefix(target, "/"):
		msg := fmt.Sprintf("error page target %q must start with '/' or be an @name", target)
		if secret {
			msg = "error page target must start with '/' or be an @name"
		}
		rep.Error(pos, diag.CodeRoutePath, msg, `write a path such as "/404.html", or a named route such as "@error"`)
	}
}

// ---- match blocks ----

var matchConditions = map[string]bool{
	"method": true, "client_ip": true, "header": true, "query": true,
	"cookie": true, "scheme": true, "host": true,
}

func validateMatch(d *ast.Directive, rep Reporter) {
	if d.Block == nil {
		return
	}
	conds, actions := 0, 0
	for _, s := range d.Block.Statements {
		if c, ok := s.(*ast.Directive); ok {
			if matchConditions[c.Name] {
				conds++
			} else {
				actions++
			}
		}
	}
	if conds == 0 {
		rep.Error(d.Pos, diag.CodeMissingRequired, "match block has no conditions",
			"add at least one of: method, client_ip, header, query, cookie, scheme, host")
	}
	if actions == 0 {
		rep.Error(d.Pos, diag.CodeMissingRequired, "match block has no action",
			"add an action such as return, try_files or php_deny")
	}
}

// validatePatternCondition checks header/query/cookie conditions: when the
// operator is a regex operator, the value must compile.
func validatePatternCondition(d *ast.Directive, rep Reporter) {
	if len(d.Args) != 3 {
		return
	}
	op, ok := d.Args[1].(*ast.Operator)
	if !ok || (op.Op != "~" && op.Op != "~*" && op.Op != "!~" && op.Op != "!~*") {
		return
	}
	pattern, ok := plainString(d.Args[2])
	if !ok {
		return
	}
	if strings.HasSuffix(op.Op, "*") {
		pattern = "(?i)" + pattern
	}
	if _, err := regexp.Compile(pattern); err != nil {
		detail := strings.TrimPrefix(err.Error(), "error parsing regexp: ")
		if s, ok := d.Args[2].(*ast.String); ok && s.Secret {
			detail = "(details hidden: the pattern comes from env() or file())"
		}
		rep.Error(d.Args[2].Position(), diag.CodeInvalidRegex,
			"invalid regular expression: "+detail,
			"LOTUS uses RE2 syntax: no backreferences or lookaround")
	}
}

// show quotes the route's path for a message, or hides it if it is secret.
func (r ParsedRoute) show() string {
	if r.Secret {
		return "(secret value)"
	}
	return fmt.Sprintf("%q", r.Path)
}
