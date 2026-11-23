package htaccess

import (
	"net/url"
	"strings"
)

// Layer is the .htaccess file of one directory on the way from the document
// root to the requested file.
type Layer struct {
	DirURL string // the directory's URL path with a trailing slash: "/" or "/blog/"
	File   *Compiled
}

// Redirect says to answer with a redirect.
type Redirect struct {
	Status   int
	Location string
}

// Rewrite says to route the request again with a new URL.
type Rewrite struct {
	Path, Query string
	// End is the END flag: no further rewriting for this request.
	End bool
}

// Decision is what the .htaccess files say about a request. At most one of
// Status, Redirect and Rewrite is set; the rest (error documents, index files,
// headers) apply to whatever response follows.
type Decision struct {
	Status   int // an error status the request must get: 403, 410, ...
	Redirect *Redirect
	Rewrite  *Rewrite

	ErrorDocs map[int]ErrorDoc
	Index     []string // DirectoryIndex; only meaningful if IndexSet
	IndexSet  bool
	Headers   []HeaderOp
	// Env holds variables set by RewriteRule's E flag, for the PHP environment,
	// and REMOTE_USER/AUTH_TYPE after a successful Basic authentication.
	Env map[string]string

	// Policy changes the response when it starts (Expires, charset, types).
	Policy *ResponsePolicy
	// PHP are php_value / php_flag settings for scripts.
	PHP []PHPSetting
	// AuthRealm is set with Status 401: the realm for WWW-Authenticate.
	AuthRealm string
	// Why says what is wrong with the configuration when Status is 500.
	Why string
}

// Evaluate applies the layers (outermost directory first) to the request.
// noRewrite is set after a rule with the END flag: rewriting is over for this request.
func Evaluate(layers []Layer, req *Request, noRewrite bool) *Decision {
	d := &Decision{ErrorDocs: map[int]ErrorDoc{}, Env: map[string]string{}}
	base := lastSegment(req.Path)

	// Settings that merge from the root downwards.
	for _, l := range layers {
		f := l.File
		for code, doc := range f.errorDocs {
			d.ErrorDocs[code] = doc
		}
		if f.indexSet {
			d.Index, d.IndexSet = f.index, true
		}
		for _, s := range f.sections {
			if s.match == nil || (base != "" && s.match(base)) {
				d.Headers = append(d.Headers, s.headers...)
			}
		}
	}

	d.Policy, d.PHP = mergeSettings(layers)

	// Access control comes first: it is decided before any rewriting, so a
	// rule can't be used to reach something the URL itself wouldn't allow
	// (the rewritten URL is checked again when it is routed).
	var access *accessSet
	for _, l := range layers {
		for _, s := range l.File.sections {
			if s.access != nil && (s.match == nil || (base != "" && s.match(base))) {
				access = s.access
			}
		}
	}
	if access != nil {
		au, authDir := mergeAuth(layers, base)
		var (
			user     string
			userDone bool
		)
		ctx := &accessCtx{ip: req.Remote, user: func() string {
			if !userDone {
				userDone = true
				if au.basic && au.fileSet && req.BasicUser != nil {
					user = req.BasicUser(au.file, authDir)
				}
			}
			return user
		}}
		ok := access.permits(ctx)
		if ctx.usesAuth && !(au.basic && au.fileSet) {
			// Require valid-user without AuthType Basic and AuthUserFile: Apache
			// answers 500 for this, and so does LOTUS (never "open").
			d.Status, d.Why = 500, "Require valid-user/user needs AuthType Basic and AuthUserFile"
			return d
		}
		if !ok {
			if ctx.needAuth {
				d.Status, d.AuthRealm = 401, au.realm
				if !au.realmSet {
					d.AuthRealm = "Restricted"
				}
			} else {
				d.Status = 403
			}
			return d
		}
		if userDone && user != "" {
			d.Env["REMOTE_USER"] = user
			d.Env["AUTH_TYPE"] = "Basic"
		}
	}

	if !noRewrite {
		for i := len(layers) - 1; i >= 0; i-- {
			rs := layers[i].File.rewrite
			if rs == nil || !rs.present {
				continue
			}
			// The deepest directory with rewrite directives decides; its rules
			// replace those of its parents (as in Apache, without RewriteOptions Inherit).
			if rs.enabled {
				res := rs.run(layers[i].DirURL, req, d.Env)
				switch res.kind {
				case rwRedirect:
					d.Redirect = &Redirect{Status: res.status, Location: res.location}
					return d
				case rwStatus:
					d.Status = res.status
					return d
				case rwRewritten:
					d.Rewrite = &Rewrite{Path: res.path, Query: res.query, End: res.end}
					return d
				}
			}
			break
		}
	}

	// mod_alias redirects: the innermost directory's first.
	ctx := &evalCtx{req: req, path: req.Path, query: req.Query, env: d.Env}
	for i := len(layers) - 1; i >= 0; i-- {
		for _, r := range layers[i].File.redirects {
			if loc, ok := r.match(ctx); ok {
				if r.gone {
					d.Status = 410
				} else {
					d.Redirect = &Redirect{Status: r.status, Location: loc}
				}
				return d
			}
		}
	}
	return d
}

func lastSegment(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// match says whether the redirect applies to the request, and where to.
func (r *redirect) match(ctx *evalCtx) (string, bool) {
	p := ctx.path
	if r.re != nil {
		m := r.re.FindStringSubmatch(p)
		if m == nil {
			return "", false
		}
		if r.gone {
			return "", true
		}
		ctx.ruleCaps = m
		return r.target.expand(ctx), true
	}
	// A prefix matches whole path segments: /a matches /a and /a/b, not /ab.
	if p != r.prefix && !strings.HasPrefix(p, r.prefix+"/") {
		return "", false
	}
	if r.gone {
		return "", true
	}
	rest := p[len(r.prefix):]
	loc := r.target.expand(ctx) + (&url.URL{Path: rest}).EscapedPath()
	if ctx.query != "" { // Redirect keeps the query string
		if strings.Contains(loc, "?") {
			loc += "&" + ctx.query
		} else {
			loc += "?" + ctx.query
		}
	}
	return loc, true
}
