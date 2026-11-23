package htaccess

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Categories group directives for the per-site allow list.
const (
	CatRewrite       = "rewrite"
	CatRedirect      = "redirect"
	CatErrorDocument = "error_document"
	CatDirectoryIdx  = "directory_index"
	CatOptions       = "options"
	CatAccess        = "access"
	CatHeaders       = "headers"
)

// Categories lists every category, in a fixed order.
var Categories = []string{CatRewrite, CatRedirect, CatErrorDocument, CatDirectoryIdx, CatOptions, CatAccess, CatHeaders}

// Options say what a compilation allows.
type Options struct {
	// Allow is the set of categories the site permits. Nil permits all.
	Allow map[string]bool
}

// Status of one directive in a file.
type Status int

const (
	Supported   Status = iota
	Ignored            // accepted, and has no effect in LOTUS
	Unsupported        // LOTUS can't do it
	Disallowed         // LOTUS can, but the site's allow list excludes it
)

func (s Status) String() string {
	return [...]string{"supported", "ignored", "unsupported", "not allowed"}[s]
}

// Line reports what became of one directive.
type Line struct {
	Line      int
	Directive string
	Status    Status
	Msg       string
	// Sensitive marks directives that grant or restrict access: an Unsupported
	// one must never be skipped, because skipping it could open something up.
	Sensitive bool
}

// ErrorDocKind is the form of an ErrorDocument.
type ErrorDocKind int

const (
	DocPath ErrorDocKind = iota // a local URL path
	DocText                     // a text to send
	DocURL                      // an external URL: redirect there
)

// ErrorDoc is the target of an ErrorDocument directive.
type ErrorDoc struct {
	Kind  ErrorDocKind
	Value string
}

// HeaderOp is one `Header` directive.
type HeaderOp struct {
	Op    string // set, setifempty, add, append, merge, unset
	Name  string
	Value string
}

type redirect struct {
	line   int
	status int
	prefix string         // Redirect: the URL path prefix
	re     *regexp.Regexp // RedirectMatch
	target template
	gone   bool
}

// section is the part of a file that applies to the whole directory, or, for
// <Files> and <FilesMatch>, to the files whose names match.
type section struct {
	match   func(base string) bool // nil: the whole directory
	headers []HeaderOp

	reqTop []*reqNode
	legacy *legacyAccess
	access *accessSet
	line   int
}

// Compiled is a .htaccess file ready to be evaluated. It is immutable.
type Compiled struct {
	Lines []Line // what became of each directive, in file order
	Notes []Note

	rewrite   *rewriteSet
	redirects []*redirect
	errorDocs map[int]ErrorDoc
	index     []string
	indexSet  bool // DirectoryIndex given (an empty list means "disabled")
	sections  []*section
}

// Note is something worth telling an administrator that isn't an error.
type Note struct {
	Line int
	Msg  string
}

// Problems returns the directives that are not usable: unsupported ones and
// those the site doesn't allow.
func (c *Compiled) Problems() []Line {
	var out []Line
	for _, l := range c.Lines {
		if l.Status == Unsupported || l.Status == Disallowed {
			out = append(out, l)
		}
	}
	return out
}

// modules are the Apache modules whose directives LOTUS implements, as written
// in <IfModule>: any of "mod_rewrite.c", "mod_rewrite", "rewrite_module".
var modules = map[string]bool{
	"core": true, "rewrite": true, "alias": true, "dir": true, "headers": true,
	"authz_core": true, "authz_host": true, "access_compat": true,
}

func normalizeModule(s string) string {
	s = strings.ToLower(s)
	s = strings.TrimSuffix(s, ".c")
	s = strings.TrimSuffix(s, "_module")
	return strings.TrimPrefix(s, "mod_")
}

// Compile turns a parsed file into a Compiled. It never fails: whatever can't
// be honoured is recorded in Lines.
func Compile(nodes []*Node, problems []Problem, opt Options) *Compiled {
	c := &compiler{opt: opt, out: &Compiled{errorDocs: map[int]ErrorDoc{}}}
	c.top = &section{}
	c.out.sections = []*section{c.top}
	for _, p := range problems {
		// A line that didn't parse might have been an access rule.
		c.out.Lines = append(c.out.Lines, Line{Line: p.Line, Directive: "(syntax)", Status: Unsupported, Msg: p.Msg, Sensitive: true})
	}
	c.nodes(nodes, c.top)
	if len(c.conds) > 0 {
		c.bad(c.condLine, "RewriteCond", "RewriteCond is not followed by a RewriteRule", false)
	}
	for _, s := range c.out.sections {
		c.finishAccess(s)
	}
	// Messages show directive names as the author wrote them.
	orig := map[int]string{}
	var walk func([]*Node)
	walk = func(ns []*Node) {
		for _, n := range ns {
			if _, dup := orig[n.Line]; !dup {
				orig[n.Line] = n.Orig
			}
			walk(n.Children)
		}
	}
	walk(nodes)
	for i := range c.out.Lines {
		l := &c.out.Lines[i]
		if o := orig[l.Line]; o != "" && strings.EqualFold(o, strings.Trim(l.Directive, "<>")) {
			l.Directive = strings.Replace(l.Directive, strings.Trim(l.Directive, "<>"), o, 1)
		}
	}
	return c.out
}

type compiler struct {
	opt      Options
	out      *Compiled
	top      *section
	conds    []*cond
	condLine int
	poisoned bool // a RewriteCond was refused: the rule it belonged to must not run
}

func (c *compiler) allowed(cat string) bool { return c.opt.Allow == nil || c.opt.Allow[cat] }

func (c *compiler) ok(n *Node) { c.add(n.Line, n.Name, Supported, "", false) }
func (c *compiler) add(line int, name string, st Status, msg string, sensitive bool) {
	c.out.Lines = append(c.out.Lines, Line{Line: line, Directive: name, Status: st, Msg: msg, Sensitive: sensitive})
}
func (c *compiler) bad(line int, name, msg string, sensitive bool) {
	c.add(line, name, Unsupported, msg, sensitive)
}

// gate checks the allow list; it records a Disallowed line and reports false
// if the category is not permitted.
func (c *compiler) gate(n *Node, cat string, sensitive bool) bool {
	if c.allowed(cat) {
		return true
	}
	c.add(n.Line, n.Name, Disallowed, fmt.Sprintf("this site does not allow %q directives (htaccess_allow)", cat), sensitive)
	return false
}

// sensitiveNames are directives that restrict or grant access. If one of them
// can't be honoured the request must fail: ignoring it would be a guess in the
// permissive direction.
var sensitiveNames = map[string]bool{
	"authtype": true, "authname": true, "authuserfile": true, "authgroupfile": true,
	"authbasicprovider": true, "authdigestprovider": true, "authmerging": true, "satisfy": true,
	"limit": true, "limitexcept": true, "require": true, "order": true, "allow": true, "deny": true,
	"requireall": true, "requireany": true, "requirenone": true, "authzsendforbiddenonfailure": true,
	"ssirequire": true, "sslrequire": true, "sslrequiressl": true, "allowoverride": true,
}

// ignored directives have no effect on a LOTUS site and are safe to skip.
var ignoredNames = map[string]string{
	"serversignature": "LOTUS never adds a version or signature line",
	"indexignore":     "directory listings are not supported, so this has no effect",
	"indexoptions":    "directory listings are not supported, so this has no effect",
}

func (c *compiler) nodes(list []*Node, sec *section) {
	for _, n := range list {
		c.node(n, sec)
	}
}

func (c *compiler) node(n *Node, sec *section) {
	if n.Container {
		c.container(n, sec)
		return
	}
	inFiles := sec != c.top
	switch n.Name {
	case "rewriteengine", "rewritebase", "rewritecond", "rewriterule", "rewriteoptions", "rewritemap", "rewritelock", "rewritelog", "rewriteloglevel":
		if inFiles {
			c.bad(n.Line, n.Name, n.Name+" is not supported inside <Files>", false)
			return
		}
		if c.gate(n, CatRewrite, false) {
			c.rewrite(n)
		}
	case "redirect", "redirectmatch", "redirectpermanent", "redirecttemp":
		if inFiles {
			c.bad(n.Line, n.Name, n.Name+" is not supported inside <Files>", false)
			return
		}
		if c.gate(n, CatRedirect, false) {
			c.redirect(n)
		}
	case "errordocument":
		if inFiles {
			c.bad(n.Line, n.Name, "ErrorDocument is not supported inside <Files>", false)
			return
		}
		if c.gate(n, CatErrorDocument, false) {
			c.errorDocument(n)
		}
	case "directoryindex":
		if inFiles {
			c.bad(n.Line, n.Name, "DirectoryIndex is not supported inside <Files>", false)
			return
		}
		if c.gate(n, CatDirectoryIdx, false) {
			c.directoryIndex(n)
		}
	case "options":
		if inFiles {
			c.bad(n.Line, n.Name, "Options is not supported inside <Files>", false)
			return
		}
		if c.gate(n, CatOptions, false) {
			c.options(n)
		}
	case "require", "order", "allow", "deny":
		if c.gate(n, CatAccess, true) {
			c.access(n, sec)
		}
	case "header":
		if c.gate(n, CatHeaders, false) {
			c.header(n, sec)
		}
	default:
		if why, ok := ignoredNames[n.Name]; ok {
			c.add(n.Line, n.Name, Ignored, why, false)
			return
		}
		c.bad(n.Line, n.Name, unsupportedHint(n.Name), sensitiveNames[n.Name])
	}
}

func unsupportedHint(name string) string {
	switch {
	case strings.HasPrefix(name, "php_"):
		return name + " is not supported yet (PHP settings belong in php.ini or the PHP pool for now)"
	case strings.HasPrefix(name, "expires"):
		return name + " is not supported yet"
	case strings.HasPrefix(name, "auth"):
		return name + " needs authentication, which is not supported yet"
	case name == "addhandler" || name == "sethandler" || name == "addtype" || name == "forcetype":
		return name + " is not supported: which files run as PHP is decided by the server's `php` route, not by .htaccess"
	}
	return "the directive " + name + " is not supported"
}

func (c *compiler) container(n *Node, sec *section) {
	switch n.Name {
	case "ifmodule":
		if len(n.Args) != 1 {
			c.bad(n.Line, "<IfModule>", "<IfModule> needs one module name", false)
			return
		}
		name := n.Args[0]
		neg := strings.HasPrefix(name, "!")
		have := modules[normalizeModule(strings.TrimPrefix(name, "!"))]
		if have != neg {
			c.nodes(n.Children, sec)
			return
		}
		if len(n.Children) > 0 {
			c.out.Notes = append(c.out.Notes, Note{n.Line, fmt.Sprintf("<IfModule %s> skipped: %s", name, "the condition is false in LOTUS")})
		}
	case "files", "filesmatch":
		if sec != c.top {
			c.bad(n.Line, "<"+n.Name+">", "<Files> blocks can't be nested", false)
			return
		}
		match, msg := filesMatcher(n)
		if msg != "" {
			// What is inside is most likely an access rule, so skipping the
			// block would leave those files open: treat it as sensitive.
			c.bad(n.Line, "<"+n.Name+">", msg, true)
			return
		}
		child := &section{match: match, line: n.Line}
		c.out.sections = append(c.out.sections, child)
		c.add(n.Line, "<"+n.Name+">", Supported, "", false)
		c.nodes(n.Children, child)
	case "requireall", "requireany", "requirenone":
		if !c.gate(n, CatAccess, true) {
			return
		}
		report := func(line int, msg string) { c.bad(line, n.Name, msg, true) }
		if k := compileRequireContainer(n, report); k != nil {
			sec.reqTop = append(sec.reqTop, k)
			c.ok(n)
		}
	default:
		c.bad(n.Line, "<"+n.Name+">", "the container <"+n.Name+"> is not supported", sensitiveNames[n.Name])
	}
}

// filesMatcher builds the name test of <Files> / <FilesMatch>.
func filesMatcher(n *Node) (func(string) bool, string) {
	args := n.Args
	if n.Name == "filesmatch" {
		if len(args) != 1 {
			return nil, "<FilesMatch> needs one regular expression"
		}
		re, msg := compileRegex(args[0], false)
		if msg != "" {
			return nil, msg
		}
		return re.MatchString, ""
	}
	switch {
	case len(args) == 1:
		if _, err := path.Match(args[0], "x"); err != nil {
			return nil, "bad file name pattern: " + err.Error()
		}
		pat := args[0]
		return func(base string) bool { ok, _ := path.Match(pat, base); return ok }, ""
	case len(args) == 2 && args[0] == "~":
		re, msg := compileRegex(args[1], false)
		if msg != "" {
			return nil, msg
		}
		return re.MatchString, ""
	}
	return nil, `<Files> needs a file name, such as <Files "wp-config.php">`
}

// ---- rewrite ----

func (c *compiler) rs() *rewriteSet {
	if c.out.rewrite == nil {
		c.out.rewrite = &rewriteSet{}
	}
	c.out.rewrite.present = true
	return c.out.rewrite
}

func (c *compiler) rewrite(n *Node) {
	switch n.Name {
	case "rewriteengine":
		if len(n.Args) != 1 || !(strings.EqualFold(n.Args[0], "on") || strings.EqualFold(n.Args[0], "off")) {
			c.bad(n.Line, n.Name, "use `RewriteEngine On` or `RewriteEngine Off`", false)
			return
		}
		c.rs().enabled = strings.EqualFold(n.Args[0], "on")
		c.ok(n)
	case "rewritebase":
		if len(n.Args) != 1 || !strings.HasPrefix(n.Args[0], "/") {
			c.bad(n.Line, n.Name, "RewriteBase needs a URL path that starts with /", false)
			return
		}
		b := n.Args[0]
		if !strings.HasSuffix(b, "/") {
			b += "/"
		}
		c.rs().base = b
		c.ok(n)
	case "rewritecond":
		cd, msg := compileCond(n.Args, n.Line)
		if msg != "" {
			c.bad(n.Line, n.Name, msg, false)
			c.conds = nil // the rule after a broken condition would run without it
			c.poisoned = true
			return
		}
		if len(c.conds) == 0 {
			c.condLine = n.Line
		}
		c.conds = append(c.conds, cd)
		c.rs()
		c.ok(n)
	case "rewriterule":
		conds := c.conds
		poisoned := c.poisoned
		c.conds, c.poisoned = nil, false
		r, msg := compileRule(n.Args, conds, n.Line)
		if msg != "" {
			c.bad(n.Line, n.Name, msg, false)
			return
		}
		if poisoned {
			// Its condition was refused, so running it without would be wrong.
			c.bad(n.Line, n.Name, "this rule depends on a RewriteCond that is not supported", false)
			return
		}
		rs := c.rs()
		rs.rules = append(rs.rules, r)
		c.ok(n)
	default:
		c.bad(n.Line, n.Name, n.Name+" is not supported", false)
	}
}

// ---- redirect ----

func statusWord(s string) (int, bool) {
	switch strings.ToLower(s) {
	case "permanent":
		return 301, true
	case "temp":
		return 302, true
	case "seeother":
		return 303, true
	case "gone":
		return 410, true
	}
	if n, err := strconv.Atoi(s); err == nil && len(s) == 3 {
		return n, true
	}
	return 0, false
}

func (c *compiler) redirect(n *Node) {
	args := n.Args
	status := 302
	switch n.Name {
	case "redirectpermanent":
		status = 301
	case "redirecttemp":
		status = 302
	default:
		if len(args) > 0 {
			if st, ok := statusWord(args[0]); ok && (len(args) > 1) {
				status, args = st, args[1:]
			}
		}
	}
	gone := status == 410
	switch {
	case gone && len(args) != 1:
		c.bad(n.Line, n.Name, "a `gone` redirect takes only the URL path", false)
		return
	case !gone && len(args) != 2:
		c.bad(n.Line, n.Name, n.Name+" needs a URL path and a target: Redirect [status] /old /new", false)
		return
	case !gone && (status < 300 || status > 399):
		c.bad(n.Line, n.Name, fmt.Sprintf("status %d is not a redirect status (use 301, 302, 303, 307 or 308; or gone)", status), false)
		return
	}
	r := &redirect{line: n.Line, status: status, gone: gone}
	if !gone {
		if !strings.HasPrefix(args[1], "/") && !hasScheme(args[1]) {
			c.bad(n.Line, n.Name, "the target must be a URL (http://...) or a path that starts with /", false)
			return
		}
		t, err := parseTemplate(args[1])
		if err != nil {
			c.bad(n.Line, n.Name, err.Error(), false)
			return
		}
		r.target = t
	}
	if n.Name == "redirectmatch" {
		re, msg := compileRegex(args[0], false)
		if msg != "" {
			c.bad(n.Line, n.Name, msg, false)
			return
		}
		r.re = re
	} else {
		if !strings.HasPrefix(args[0], "/") {
			c.bad(n.Line, n.Name, "the URL path must start with /", false)
			return
		}
		r.prefix = strings.TrimSuffix(args[0], "/")
		if r.prefix == "" && args[0] != "/" {
			r.prefix = args[0]
		}
	}
	c.out.redirects = append(c.out.redirects, r)
	c.ok(n)
}

// ---- error documents, directory index, options ----

func (c *compiler) errorDocument(n *Node) {
	if len(n.Args) != 2 {
		c.bad(n.Line, n.Name, `ErrorDocument needs a status and a target: ErrorDocument 404 /errors/404.html`, false)
		return
	}
	code, err := strconv.Atoi(n.Args[0])
	if err != nil || code < 300 || code > 599 {
		c.bad(n.Line, n.Name, "the first argument must be a status code (400-599)", false)
		return
	}
	v := n.Args[1]
	var doc ErrorDoc
	switch {
	case n.Quoted[1]:
		doc = ErrorDoc{DocText, v}
	case strings.HasPrefix(v, "/"):
		doc = ErrorDoc{DocPath, v}
	case hasScheme(v):
		doc = ErrorDoc{DocURL, v}
	default:
		c.bad(n.Line, n.Name, "the target must be a path that starts with /, a URL, or text in quotes", false)
		return
	}
	c.out.errorDocs[code] = doc
	c.ok(n)
}

func (c *compiler) directoryIndex(n *Node) {
	if len(n.Args) == 0 {
		c.bad(n.Line, n.Name, "DirectoryIndex needs at least one file name", false)
		return
	}
	if len(n.Args) == 1 && strings.EqualFold(n.Args[0], "disabled") {
		c.out.index, c.out.indexSet = nil, true
		c.ok(n)
		return
	}
	for _, a := range n.Args {
		if strings.ContainsAny(a, `/\`) || a == "." || a == ".." || strings.ContainsRune(a, 0) {
			c.bad(n.Line, n.Name, fmt.Sprintf("%q: only plain file names are supported, not paths", a), false)
			return
		}
	}
	c.out.index, c.out.indexSet = append([]string(nil), n.Args...), true
	c.ok(n)
}

func (c *compiler) options(n *Node) {
	if len(n.Args) == 0 {
		c.bad(n.Line, n.Name, "Options needs at least one option", false)
		return
	}
	var notes []string
	for _, a := range n.Args {
		sign := byte(0)
		name := a
		if a[0] == '+' || a[0] == '-' {
			sign, name = a[0], a[1:]
		}
		off := sign == '-'
		switch strings.ToLower(name) {
		case "indexes", "multiviews", "execcgi", "includes", "includesnoexec":
			if !off && !strings.EqualFold(name, "includesnoexec") {
				c.bad(n.Line, n.Name, fmt.Sprintf("Options %s is not supported (no directory listings, content negotiation, CGI or server-side includes)", a), false)
				return
			}
			if !off {
				c.bad(n.Line, n.Name, "Options "+a+" is not supported", false)
				return
			}
		case "followsymlinks", "symlinksifownermatch":
			notes = append(notes, a+" has no effect: the site's `symlinks` setting decides")
		case "none":
		case "all":
			c.bad(n.Line, n.Name, "Options All is not supported (it would turn on directory listings)", false)
			return
		default:
			c.bad(n.Line, n.Name, fmt.Sprintf("the option %q is not supported", a), false)
			return
		}
	}
	if len(notes) > 0 {
		c.add(n.Line, n.Name, Ignored, strings.Join(notes, "; "), false)
		return
	}
	c.ok(n)
}

// ---- headers ----

var headerNameChars = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

func (c *compiler) header(n *Node, sec *section) {
	args := n.Args
	if len(args) > 0 && (strings.EqualFold(args[0], "always") || strings.EqualFold(args[0], "onsuccess")) {
		args = args[1:] // LOTUS applies headers to every response
	}
	if len(args) < 2 {
		c.bad(n.Line, n.Name, "use: Header set|unset|append|add|merge|setifempty Name [value]", false)
		return
	}
	op := strings.ToLower(args[0])
	switch op {
	case "set", "setifempty", "add", "append", "merge", "unset":
	default:
		c.bad(n.Line, n.Name, fmt.Sprintf("Header %s is not supported", op), false)
		return
	}
	name := args[1]
	if !headerNameChars.MatchString(name) {
		c.bad(n.Line, n.Name, fmt.Sprintf("%q is not a valid header name", name), false)
		return
	}
	h := HeaderOp{Op: op, Name: name}
	rest := args[2:]
	if op == "unset" {
		if len(rest) != 0 {
			c.bad(n.Line, n.Name, "Header unset takes only the header name", false)
			return
		}
	} else {
		if len(rest) != 1 {
			if len(rest) == 2 && strings.HasPrefix(strings.ToLower(rest[1]), "env=") {
				c.bad(n.Line, n.Name, "the env= condition of Header is not supported", false)
				return
			}
			c.bad(n.Line, n.Name, "Header "+op+" needs a name and one value", false)
			return
		}
		h.Value = rest[0]
		if strings.Contains(h.Value, "%{") || strings.Contains(h.Value, "%t") || strings.Contains(h.Value, "%D") {
			c.bad(n.Line, n.Name, "expressions such as %{VAR}e in a header value are not supported", false)
			return
		}
		for i := 0; i < len(h.Value); i++ {
			if h.Value[i] < 0x20 && h.Value[i] != '\t' || h.Value[i] == 0x7f {
				c.bad(n.Line, n.Name, "a header value can't contain control characters", false)
				return
			}
		}
	}
	sec.headers = append(sec.headers, h)
	c.ok(n)
}

// ---- access ----

func (c *compiler) access(n *Node, sec *section) {
	switch n.Name {
	case "require":
		leaf, msg := requireLeaf(n.Args, false)
		if msg != "" {
			c.bad(n.Line, n.Name, msg, true)
			return
		}
		sec.reqTop = append(sec.reqTop, leaf)
	case "order":
		if len(n.Args) != 1 {
			c.bad(n.Line, n.Name, "use `Order Allow,Deny` or `Order Deny,Allow`", true)
			return
		}
		if sec.legacy == nil {
			sec.legacy = &legacyAccess{}
		}
		switch strings.ToLower(strings.ReplaceAll(n.Args[0], " ", "")) {
		case "allow,deny":
			sec.legacy.allowFirst = true
		case "deny,allow":
		default:
			c.bad(n.Line, n.Name, "use `Order Allow,Deny` or `Order Deny,Allow`", true)
			return
		}
		sec.legacy.orderSet = true
	case "allow", "deny":
		if len(n.Args) < 2 || !strings.EqualFold(n.Args[0], "from") {
			c.bad(n.Line, n.Name, "use: "+strings.ToUpper(n.Name[:1])+n.Name[1:]+" from all|address...", true)
			return
		}
		words := n.Args[1:]
		for _, w := range words {
			if strings.HasPrefix(strings.ToLower(w), "env=") {
				c.bad(n.Line, n.Name, "the env= form of "+n.Name+" is not supported", true)
				return
			}
		}
		if sec.legacy == nil {
			sec.legacy = &legacyAccess{}
		}
		l := sec.legacy
		l.rulesFound = true
		if len(words) == 1 && strings.EqualFold(words[0], "all") {
			if n.Name == "allow" {
				l.allowAll = true
			} else {
				l.denyAll = true
			}
			break
		}
		nets, err := parseNets(words)
		if err != nil {
			c.bad(n.Line, n.Name, err.Error(), true)
			return
		}
		if n.Name == "allow" {
			l.allow = append(l.allow, nets...)
		} else {
			l.deny = append(l.deny, nets...)
		}
	}
	c.ok(n)
}

// finishAccess turns what a section collected into its access set.
func (c *compiler) finishAccess(s *section) {
	hasReq := len(s.reqTop) > 0
	hasLegacy := s.legacy != nil
	switch {
	case hasReq && hasLegacy:
		c.bad(s.line, "Require", "Require and Order/Allow/Deny are mixed in one block; use one or the other", true)
	case hasReq:
		s.access = &accessSet{line: s.line, top: &reqNode{kind: rqAny, kids: s.reqTop}}
	case hasLegacy:
		s.access = &accessSet{line: s.line, legacy: s.legacy}
	}
}
