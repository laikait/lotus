// Package nginxconv translates an Nginx configuration into a LOTUS one
// (`lotus compat nginx convert`).
//
// It reads the directives that decide what a site does (server, location,
// proxy_pass, fastcgi_pass, try_files, rewrite, return, gzip, caches, limits,
// TLS files) and writes the same behavior in the LOTUS language. It never
// guesses silently: every directive is reported as translated, approximated
// (it works, but not exactly as in Nginx), ignored (it has no effect in LOTUS)
// or unsupported (its behavior is lost), and the unsupported ones are also
// left in the output as comments, so the result can be reviewed in one place.
package nginxconv

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Node is one Nginx directive: a name, its arguments and, for a context such
// as `server` or `location`, the directives inside it.
type Node struct {
	Name  string
	Args  []string
	Block []*Node // nil for a simple directive
	IsCtx bool    // had a { } block (even an empty one)
	File  string
	Line  int
}

// Options controls how files are read.
type Options struct {
	// ReadFile and Glob default to the operating system's.
	ReadFile func(path string) ([]byte, error)
	Glob     func(pattern string) ([]string, error)
}

const maxIncludeDepth = 16

type token struct {
	text   string
	kind   byte // 'w' word, ';', '{', '}'
	line   int
	quoted bool
}

// lex splits Nginx configuration text into words and punctuation.
func lex(src string) ([]token, error) {
	var toks []token
	line := 1
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == '\n':
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '#':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == ';' || c == '{' || c == '}':
			toks = append(toks, token{text: string(c), kind: c, line: line})
			i++
		case c == '"' || c == '\'':
			start := line
			q := c
			i++
			var b strings.Builder
			closed := false
			for i < len(src) {
				ch := src[i]
				if ch == '\\' && i+1 < len(src) {
					n := src[i+1]
					switch n {
					case '"', '\'', '\\':
						b.WriteByte(n)
					case 't':
						b.WriteByte('\t')
					case 'r':
						b.WriteByte('\r')
					case 'n':
						b.WriteByte('\n')
					default:
						b.WriteByte('\\')
						b.WriteByte(n)
					}
					i += 2
					continue
				}
				if ch == q {
					closed = true
					i++
					break
				}
				if ch == '\n' {
					line++
				}
				b.WriteByte(ch)
				i++
			}
			if !closed {
				return nil, fmt.Errorf("line %d: a quoted string is never closed", start)
			}
			toks = append(toks, token{text: b.String(), kind: 'w', line: start, quoted: true})
		default:
			start := i
			for i < len(src) {
				ch := src[i]
				if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' || ch == ';' {
					break
				}
				if ch == '{' {
					// ${name} is a variable, not a block.
					if i > start && src[i-1] == '$' {
						for i < len(src) && src[i] != '}' && src[i] != '\n' {
							i++
						}
						if i < len(src) && src[i] == '}' {
							i++
						}
						continue
					}
					break
				}
				if ch == '}' {
					break
				}
				i++
			}
			toks = append(toks, token{text: src[start:i], kind: 'w', line: line})
		}
	}
	return toks, nil
}

// Parse reads the file and everything it includes.
func Parse(path string, opts Options) ([]*Node, error) {
	if opts.ReadFile == nil {
		opts.ReadFile = os.ReadFile
	}
	if opts.Glob == nil {
		opts.Glob = filepath.Glob
	}
	p := &parser{opts: opts, root: filepath.Dir(path)}
	return p.file(path, 0)
}

type parser struct {
	opts Options
	root string // directory relative include paths start from
}

func (p *parser) file(path string, depth int) ([]*Node, error) {
	data, err := p.opts.ReadFile(path)
	if err != nil {
		return nil, err
	}
	toks, err := lex(strings.TrimPrefix(string(data), "\xef\xbb\xbf"))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	pos := 0
	nodes, err := p.block(path, toks, &pos, depth, false)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return nodes, nil
}

// block parses statements until the closing brace (or the end of the file).
func (p *parser) block(file string, toks []token, pos *int, depth int, inner bool) ([]*Node, error) {
	var out []*Node
	for *pos < len(toks) {
		t := toks[*pos]
		switch t.kind {
		case '}':
			if !inner {
				return nil, fmt.Errorf("line %d: unexpected \"}\"", t.line)
			}
			*pos++
			return out, nil
		case ';':
			*pos++ // an empty statement
			continue
		case '{':
			return nil, fmt.Errorf("line %d: unexpected \"{\"", t.line)
		}
		n := &Node{Name: t.text, File: file, Line: t.line}
		*pos++
		for *pos < len(toks) && toks[*pos].kind == 'w' {
			n.Args = append(n.Args, toks[*pos].text)
			*pos++
		}
		if *pos >= len(toks) {
			return nil, fmt.Errorf("line %d: %q has no \";\" or block", t.line, n.Name)
		}
		switch toks[*pos].kind {
		case ';':
			*pos++
		case '{':
			*pos++
			if depth > maxIncludeDepth*4 {
				return nil, fmt.Errorf("line %d: blocks are nested too deeply", t.line)
			}
			body, err := p.block(file, toks, pos, depth, true)
			if err != nil {
				return nil, err
			}
			n.Block, n.IsCtx = body, true
		default:
			return nil, fmt.Errorf("line %d: %q is not closed by \";\" or a block", t.line, n.Name)
		}
		if n.Name == "include" && !n.IsCtx && len(n.Args) == 1 {
			spliced, handled, err := p.include(n, depth)
			if err != nil {
				return nil, err
			}
			if handled {
				out = append(out, spliced...)
				continue
			}
		}
		out = append(out, n)
	}
	if inner {
		return nil, fmt.Errorf("a block is never closed")
	}
	return out, nil
}

// include splices the files an include names. Files Nginx ships with
// (mime.types, fastcgi_params, ...) are left as include nodes: the converter
// reports them as built in. A pattern that matches nothing is not an error.
func (p *parser) include(n *Node, depth int) ([]*Node, bool, error) {
	arg := n.Args[0]
	if builtinInclude(arg) {
		return nil, false, nil
	}
	if depth >= maxIncludeDepth {
		return nil, false, fmt.Errorf("%s:%d: includes are nested more than %d deep", n.File, n.Line, maxIncludeDepth)
	}
	pattern := arg
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(p.root, pattern)
	}
	var paths []string
	if strings.ContainsAny(arg, "*?[") {
		m, err := p.opts.Glob(pattern)
		if err != nil {
			return nil, false, fmt.Errorf("%s:%d: %w", n.File, n.Line, err)
		}
		sort.Strings(m)
		paths = m
	} else {
		paths = []string{pattern}
	}
	var out []*Node
	for _, f := range paths {
		nodes, err := p.file(f, depth+1)
		if err != nil {
			// Report the include that failed; the converter turns this into an
			// issue and goes on.
			return []*Node{{Name: "include", Args: n.Args, File: n.File, Line: n.Line, Block: []*Node{{Name: "#error", Args: []string{err.Error()}}}}}, true, nil
		}
		out = append(out, nodes...)
	}
	return out, true, nil
}

func builtinInclude(arg string) bool {
	switch filepath.Base(arg) {
	case "mime.types", "fastcgi_params", "fastcgi.conf", "proxy_params", "scgi_params", "uwsgi_params", "koi-utf", "koi-win", "win-utf":
		return true
	}
	return false
}
