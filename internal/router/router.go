// Package router picks the route for a request. It implements
// docs/architecture/routing-precedence.md: routes are chosen by path alone
// (the Nginx location model), and non-path conditions live in match blocks
// inside the chosen route.
package router

import (
	"path"
	"strings"

	"github.com/laikait/lotus/internal/config/model"
)

// Found is the result of path matching.
type Found struct {
	Route *model.Route
	// Captures holds the regex submatches (index 0 is the whole match) when the
	// route is a regex route. It is nil otherwise.
	Captures []string
}

// NormalizePath returns the path routing and file lookup work on: always
// rooted, with "." and ".." resolved and repeated slashes merged. A trailing
// slash is kept, because "/a/" and "/a" can mean different things.
func NormalizePath(p string) string {
	if p == "" {
		return "/"
	}
	trailing := strings.HasSuffix(p, "/")
	c := path.Clean("/" + p)
	if trailing && c != "/" {
		c += "/"
	}
	return c
}

// Find picks the route for p among the routes of one level (a site's routes).
// ok is false if no route applies; the caller then runs the site's default
// handler. p must be normalized.
func Find(level []model.Route, p string) (f Found, ok bool) {
	// 1. An exact route wins outright.
	for i := range level {
		if level[i].Kind == model.RouteExact && level[i].Path == p {
			return Found{Route: &level[i]}, true
		}
	}

	// 2. The longest matching prefix route.
	var best *model.Route
	for i := range level {
		r := &level[i]
		if (r.Kind == model.RoutePrefix || r.Kind == model.RouteFinalPrefix) &&
			strings.HasPrefix(p, r.Path) && (best == nil || len(r.Path) > len(best.Path)) {
			best = r
		}
	}
	chosen := Found{}
	final := false
	if best != nil {
		chosen = Found{Route: best}
		final = best.Kind == model.RouteFinalPrefix
	}

	// 3. A matching prefix may have routes of its own. A deeper exact, regex
	// or final-prefix decision wins; a deeper plain prefix refines the choice.
	if best != nil && len(best.Children) > 0 {
		if deeper, found := Find(best.Children, p); found {
			switch deeper.Route.Kind {
			case model.RoutePrefix:
				chosen = deeper
			default:
				return deeper, true
			}
		}
	}

	// 4. Unless the prefix was final, regex routes get a chance, in order.
	if !final {
		for i := range level {
			r := &level[i]
			if (r.Kind == model.RouteRegex || r.Kind == model.RouteRegexFold) && r.Regexp != nil {
				if m := r.Regexp.FindStringSubmatch(p); m != nil {
					return Found{Route: r, Captures: m}, true
				}
			}
		}
	}

	// 5. Otherwise the prefix, if there is one.
	return chosen, chosen.Route != nil
}

// FindNamed returns the named route "@name", or nil.
func FindNamed(named []model.Route, name string) *model.Route {
	for i := range named {
		if named[i].Path == name {
			return &named[i]
		}
	}
	return nil
}
