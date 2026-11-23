package htaccess

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// The rewrite engine follows mod_rewrite in per-directory context, which is
// what a .htaccess file is:
//
//   - A rule's pattern is matched against the URL path relative to the
//     directory of the .htaccess file, without a leading slash.
//   - A substitution starting with "/" is a URL path; any other relative
//     substitution is added to RewriteBase (by default the directory's URL).
//   - When a rule changes the URL, the request goes through the whole server
//     again with the new URL (an internal redirect), so the same rules see it a
//     second time: that is why the usual files guard themselves with
//     "RewriteRule ^index\.php$ - [L]" and "RewriteCond %{REQUEST_FILENAME} !-f".
//     The server limits how many times that can happen.
//   - Rules from a parent directory are not inherited by a child directory that
//     has rewrite directives of its own.

type condOp int

const (
	opRegex condOp = iota
	opFile         // -f
	opDir          // -d
	opSize         // -s
	opStrEq
	opStrLt
	opStrGt
	opStrLe
	opStrGe
)

type cond struct {
	line int
	test template
	op   condOp
	re   *regexp.Regexp
	arg  string // for string and integer comparisons
	neg  bool
	nc   bool // [NC]: string comparisons ignore case (regexes carry it themselves)
	or   bool // [OR]: combined with the next condition by OR instead of AND
}

type envSet struct {
	name  string
	value template
	unset bool
}

type rule struct {
	line  int
	re    *regexp.Regexp
	neg   bool // pattern written with a leading !
	sub   template
	dash  bool // substitution "-": no change
	conds []*cond

	last, end, noescape, qsa, qsd, chain bool
	redirect                             int // R: status, 0 = not set
	forbid, gone                         bool
	skip                                 int
	env                                  []envSet
}

type rewriteSet struct {
	present bool // the file has rewrite directives at all
	enabled bool // RewriteEngine On
	base    string
	rules   []*rule
}

// ---- compiling ----

// splitFlags splits "[L,R=301]" into its flags: name (lower case) and value.
func splitFlags(s string) (flags []flag, ok bool) {
	if len(s) < 2 || s[0] != '[' || s[len(s)-1] != ']' {
		return nil, false
	}
	for _, f := range strings.Split(s[1:len(s)-1], ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		name, val, has := strings.Cut(f, "=")
		flags = append(flags, flag{strings.ToLower(strings.TrimSpace(name)), val, has})
	}
	return flags, true
}

type flag struct {
	name, val string
	hasVal    bool
}

// compileRegex compiles an Apache (PCRE) pattern with Go's RE2. Patterns that
// need PCRE-only features (lookahead, back-references) are refused with a message.
func compileRegex(pattern string, nocase bool) (*regexp.Regexp, string) {
	if nocase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Sprintf("the pattern %q is not supported (LOTUS uses RE2 syntax: no lookahead, look-behind or back-references): %v", strings.TrimPrefix(pattern, "(?i)"), err)
	}
	return re, ""
}

// compileCond turns `RewriteCond TestString CondPattern [flags]` into a cond.
func compileCond(args []string, line int) (*cond, string) {
	if len(args) < 2 || len(args) > 3 {
		return nil, "RewriteCond needs a test string, a pattern and optional flags"
	}
	c := &cond{line: line}
	test, err := parseTemplate(args[0])
	if err != nil {
		return nil, err.Error()
	}
	c.test = test
	nocase := false
	if len(args) == 3 {
		flags, ok := splitFlags(args[2])
		if !ok {
			return nil, fmt.Sprintf("bad flags %q (they must look like [NC,OR])", args[2])
		}
		for _, f := range flags {
			switch f.name {
			case "nc", "nocase":
				nocase = true
			case "or", "ornext":
				c.or = true
			case "nv", "novary":
			default:
				return nil, fmt.Sprintf("the RewriteCond flag %q is not supported", f.name)
			}
		}
	}

	pat := args[1]
	if strings.HasPrefix(pat, "!") {
		c.neg, pat = true, pat[1:]
	}
	switch {
	case pat == "-f" || pat == "-F":
		if pat == "-F" {
			return nil, "RewriteCond -F (subrequest) is not supported"
		}
		c.op = opFile
	case pat == "-d":
		c.op = opDir
	case pat == "-s":
		c.op = opSize
	case pat == "-l" || pat == "-L" || pat == "-h" || pat == "-x" || pat == "-U":
		return nil, fmt.Sprintf("RewriteCond %s is not supported (only -f, -d and -s file tests)", pat)
	case strings.HasPrefix(pat, "-") && len(pat) == 3 && pat[1] >= 'a' && pat[1] <= 'z' && pat[2] >= 'a' && pat[2] <= 'z':
		// "-f"-like words we don't know are most likely file tests; refuse them
		// rather than read them as the regular expression "-xx".
		return nil, fmt.Sprintf("RewriteCond %s is not supported", pat)
	case strings.HasPrefix(pat, "<="):
		c.op, c.arg = opStrLe, pat[2:]
	case strings.HasPrefix(pat, ">="):
		c.op, c.arg = opStrGe, pat[2:]
	case strings.HasPrefix(pat, "<"):
		c.op, c.arg = opStrLt, pat[1:]
	case strings.HasPrefix(pat, ">"):
		c.op, c.arg = opStrGt, pat[1:]
	case strings.HasPrefix(pat, "="):
		c.op, c.arg = opStrEq, pat[1:]
	default:
		c.op = opRegex
		var msg string
		if c.re, msg = compileRegex(pat, nocase); msg != "" {
			return nil, msg
		}
	}
	if nocase && c.op >= opStrEq && c.op <= opStrGe {
		c.nc, c.arg = true, strings.ToLower(c.arg)
	}
	return c, ""
}

// compileRule turns `RewriteRule Pattern Substitution [flags]` into a rule.
func compileRule(args []string, conds []*cond, line int) (*rule, string) {
	if len(args) < 2 || len(args) > 3 {
		return nil, "RewriteRule needs a pattern, a substitution and optional flags"
	}
	r := &rule{line: line, conds: conds}
	pat := args[0]
	if strings.HasPrefix(pat, "!") {
		r.neg, pat = true, pat[1:]
	}
	nocase := false
	var flags []flag
	if len(args) == 3 {
		var ok bool
		if flags, ok = splitFlags(args[2]); !ok {
			return nil, fmt.Sprintf("bad flags %q (they must look like [L,R=301])", args[2])
		}
	}
	for _, f := range flags {
		switch f.name {
		case "l", "last":
			r.last = true
		case "end":
			r.end, r.last = true, true
		case "nc", "nocase":
			nocase = true
		case "ne", "noescape":
			r.noescape = true
		case "qsa", "qsappend":
			r.qsa = true
		case "qsd", "qsdiscard":
			r.qsd = true
		case "pt", "passthrough":
			r.last = true
		case "c", "chain":
			r.chain = true
		case "nosubreq", "ns":
		case "r", "redirect":
			r.redirect = 302
			if f.hasVal {
				code, ok := atoi(f.val)
				if !ok || code < 100 || code > 599 {
					return nil, fmt.Sprintf("bad redirect status in R=%s", f.val)
				}
				r.redirect = code
			}
			r.last = true
		case "f", "forbidden":
			r.forbid, r.last = true, true
		case "g", "gone":
			r.gone, r.last = true, true
		case "s", "skip":
			n, ok := atoi(f.val)
			if !f.hasVal || !ok || n < 0 || n > 100 {
				return nil, "the S flag needs a number of rules to skip: [S=2]"
			}
			r.skip = n
		case "e", "env":
			if !f.hasVal || f.val == "" {
				return nil, "the E flag needs a variable: [E=NAME:value]"
			}
			name, val, _ := strings.Cut(f.val, ":")
			es := envSet{name: name}
			if strings.HasPrefix(name, "!") {
				es.name, es.unset = name[1:], true
			} else {
				if !f.hasVal {
					val = "1"
				}
				t, err := parseTemplate(val)
				if err != nil {
					return nil, err.Error()
				}
				es.value = t
			}
			if !validEnvName(es.name) {
				return nil, fmt.Sprintf("%q is not a valid environment variable name", es.name)
			}
			r.env = append(r.env, es)
		default:
			return nil, fmt.Sprintf("the RewriteRule flag %q is not supported", f.name)
		}
	}
	var msg string
	if r.re, msg = compileRegex(pat, nocase); msg != "" {
		return nil, msg
	}
	if args[1] == "-" {
		r.dash = true
	} else {
		t, err := parseTemplate(args[1])
		if err != nil {
			return nil, err.Error()
		}
		r.sub = t
	}
	return r, ""
}

func validEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// ---- running ----

type rewriteKind int

const (
	rwNone      rewriteKind = iota
	rwRewritten             // the URL changed: route again
	rwRedirect              // answer with a redirect
	rwStatus                // answer with a status (F, G, R=4xx)
)

type rewriteResult struct {
	kind     rewriteKind
	path     string
	query    string
	end      bool // the END flag: no more rewriting for this request
	status   int
	location string
}

func stripDir(path, dirURL string) string {
	if path+"/" == dirURL {
		return ""
	}
	if strings.HasPrefix(path, dirURL) {
		return path[len(dirURL):]
	}
	return strings.TrimPrefix(path, "/")
}

func hasScheme(s string) bool {
	i := strings.Index(s, "://")
	if i <= 0 {
		return false
	}
	for j := 0; j < i; j++ {
		c := s[j]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || j > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.')) {
			return false
		}
	}
	return true
}

func escapePath(p string) string { return (&url.URL{Path: p}).EscapedPath() }

func (c *cond) eval(ctx *evalCtx) bool {
	s := c.test.expand(ctx)
	var ok bool
	switch c.op {
	case opRegex:
		m := c.re.FindStringSubmatch(s)
		ok = m != nil
		if ok && !c.neg {
			ctx.condCaps = m
		}
	case opFile, opDir, opSize:
		ok = ctx.fileTest(c.op, s)
	case opStrEq, opStrLt, opStrGt, opStrLe, opStrGe:
		if c.nc {
			s = strings.ToLower(s)
		}
		ok = compareStrings(c.op, s, c.arg)
	}
	return ok != c.neg
}

func compareStrings(op condOp, a, b string) bool {
	switch op {
	case opStrEq:
		return a == b
	case opStrLt:
		return a < b
	case opStrGt:
		return a > b
	case opStrLe:
		return a <= b
	case opStrGe:
		return a >= b
	}
	return false
}

// fileTest answers -f, -d and -s for a file name. Only names below the
// document root can be tested; anything else is "does not exist", so a rule can
// never be used to learn about files elsewhere on the machine.
func (c *evalCtx) fileTest(op condOp, name string) bool {
	root := c.req.Root
	var urlPath string
	switch {
	case name == root:
		urlPath = "/"
	case strings.HasPrefix(name, root+"/"):
		urlPath = name[len(root):]
	default:
		return false
	}
	if c.req.Stat == nil {
		return false
	}
	// "/dir/" names a directory only: a regular file with a slash after it
	// does not exist, as with stat(2).
	wantDir := len(urlPath) > 1 && strings.HasSuffix(urlPath, "/")
	urlPath = strings.TrimRight(urlPath, "/")
	if urlPath == "" {
		urlPath = "/"
	}
	info, ok := c.req.Stat(urlPath)
	if !ok || (wantDir && !info.IsDir) {
		return false
	}
	switch op {
	case opFile:
		return !info.IsDir
	case opDir:
		return info.IsDir
	case opSize:
		return !info.IsDir && info.Size > 0
	}
	return false
}

// condsHold evaluates a rule's conditions: all of them, except that [OR]
// joins a condition to the next one by OR.
func (r *rule) condsHold(ctx *evalCtx) bool {
	result, orPending := true, false
	for _, c := range r.conds {
		v := c.eval(ctx)
		if orPending {
			result = result || v
		} else {
			result = result && v
		}
		orPending = c.or
	}
	return result
}

// run applies the rules to the request in the directory dirURL ("/" or "/a/b/").
func (rs *rewriteSet) run(dirURL string, req *Request, env map[string]string) rewriteResult {
	ctx := &evalCtx{req: req, path: req.Path, query: req.Query, env: env}
	base := dirURL
	if rs.base != "" {
		base = rs.base
	}
	changed := false
	skipping := false
	for i := 0; i < len(rs.rules); i++ {
		r := rs.rules[i]
		if skipping { // the rule it was chained to didn't apply
			if !r.chain {
				skipping = false
			}
			continue
		}

		rel := stripDir(ctx.path, dirURL)
		m := r.re.FindStringSubmatch(rel)
		matched := (m != nil) != r.neg
		ctx.ruleCaps, ctx.condCaps = nil, nil
		if matched && !r.neg {
			ctx.ruleCaps = m
		}
		if matched && !r.condsHold(ctx) {
			matched = false
		}
		if !matched {
			if r.chain {
				skipping = true
			}
			continue
		}

		for _, e := range r.env {
			if e.unset {
				delete(ctx.env, e.name)
			} else {
				ctx.env[e.name] = e.value.expand(ctx)
			}
		}
		if r.forbid {
			return rewriteResult{kind: rwStatus, status: 403}
		}
		if r.gone {
			return rewriteResult{kind: rwStatus, status: 410}
		}

		if !r.dash {
			sub := r.sub.expand(ctx)
			path, query, hasQuery := sub, "", false
			if i := strings.IndexByte(sub, '?'); i >= 0 {
				path, query, hasQuery = sub[:i], sub[i+1:], true
			}
			switch {
			case hasQuery && r.qsa && ctx.query != "":
				query += "&" + ctx.query
			case !hasQuery && !r.qsd:
				query = ctx.query
			}

			if hasScheme(path) { // an absolute URL: always a redirect
				status := r.redirect
				if status == 0 {
					status = 302
				}
				loc := path
				if query != "" {
					loc += "?" + query
				}
				return rewriteResult{kind: rwRedirect, status: status, location: loc}
			}
			if !strings.HasPrefix(path, "/") {
				path = base + path
			}
			if r.redirect != 0 {
				loc := path
				if !r.noescape {
					loc = escapePath(path)
				}
				if query != "" {
					loc += "?" + query
				}
				if r.redirect >= 300 && r.redirect < 400 {
					return rewriteResult{kind: rwRedirect, status: r.redirect, location: loc}
				}
				return rewriteResult{kind: rwStatus, status: r.redirect}
			}
			if path != ctx.path || query != ctx.query {
				changed = true
			}
			ctx.path, ctx.query = path, query
		} else if r.redirect != 0 {
			// R with "-" has nothing to redirect to; Apache answers with the status.
			return rewriteResult{kind: rwStatus, status: r.redirect}
		}

		if r.skip > 0 {
			i += r.skip
		}
		if r.last {
			if r.end {
				return rs.finish(changed, ctx, true)
			}
			break
		}
	}
	return rs.finish(changed, ctx, false)
}

func (rs *rewriteSet) finish(changed bool, ctx *evalCtx, end bool) rewriteResult {
	if !changed {
		return rewriteResult{kind: rwNone, end: end}
	}
	return rewriteResult{kind: rwRewritten, path: ctx.path, query: ctx.query, end: end}
}
