// Package htaccess reads Apache .htaccess files and applies the subset of them
// that LOTUS supports.
//
// A file is parsed and compiled once (Parse, Compile) into a Compiled value
// that never changes, and then evaluated for each request (Evaluate). Nothing
// in a .htaccess file is executed as code: it is data that selects among
// behaviours LOTUS already has. Whatever LOTUS can't honour is recorded as an
// Issue, never silently dropped, so the server can refuse the request, and
// `lotus compat htaccess check` can tell an administrator before a site goes live.
package htaccess

import (
	"fmt"
	"strings"
)

// Limits on what a file may contain. A .htaccess file is written by a tenant, so
// it is parsed as untrusted input.
const (
	MaxFileSize   = 256 << 10
	maxDirectives = 5000
	maxDepth      = 8
	maxLineLen    = 16 << 10
)

// Node is a directive or a container (<IfModule ...>) of a file.
type Node struct {
	Name   string   // lower-case: the directive name, or the container name without brackets
	Orig   string   // the name as written, for messages
	Args   []string // arguments with quotes and escapes processed
	Quoted []bool   // whether each argument was written in quotes
	Line   int      // 1-based line where it starts

	Container bool
	Children  []*Node
}

// Problem is a syntax error.
type Problem struct {
	Line int
	Msg  string
}

func (p Problem) String() string { return fmt.Sprintf("line %d: %s", p.Line, p.Msg) }

// Parse reads the file's syntax: logical lines (a trailing backslash joins the
// next line), full-line # comments, quoted arguments, and nested containers.
func Parse(src []byte) ([]*Node, []Problem) {
	p := &parser{}
	if len(src) > MaxFileSize {
		return nil, []Problem{{1, fmt.Sprintf("the file is larger than %d bytes", MaxFileSize)}}
	}
	lines := strings.Split(string(src), "\n")
	var top []*Node
	// stack of open containers; top-level nodes go to top
	type frame struct {
		node *Node
	}
	var stack []frame
	add := func(n *Node) {
		if len(stack) == 0 {
			top = append(top, n)
		} else {
			parent := stack[len(stack)-1].node
			parent.Children = append(parent.Children, n)
		}
	}

	for i := 0; i < len(lines); i++ {
		start := i + 1
		line := strings.TrimRight(lines[i], "\r")
		// A backslash at the end of a line continues it.
		for strings.HasSuffix(line, "\\") && i+1 < len(lines) {
			if len(line) > maxLineLen {
				break
			}
			i++
			line = line[:len(line)-1] + strings.TrimRight(lines[i], "\r")
		}
		if len(line) > maxLineLen {
			p.problem(start, "line is too long")
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		if p.count++; p.count > maxDirectives {
			p.problem(start, fmt.Sprintf("more than %d directives", maxDirectives))
			break
		}

		if line[0] == '<' {
			if !strings.HasSuffix(line, ">") {
				p.problem(start, "a line that starts with '<' must end with '>'")
				continue
			}
			inner := strings.TrimSpace(line[1 : len(line)-1])
			if strings.HasPrefix(inner, "/") { // closing tag
				name := strings.ToLower(strings.TrimSpace(inner[1:]))
				if len(stack) == 0 || stack[len(stack)-1].node.Name != name {
					p.problem(start, fmt.Sprintf("</%s> does not close an open <%s>", name, name))
					continue
				}
				stack = stack[:len(stack)-1]
				continue
			}
			words, quoted, err := splitWords(inner)
			if err != "" || len(words) == 0 {
				p.problem(start, "bad container: "+err)
				continue
			}
			if len(stack) >= maxDepth {
				p.problem(start, fmt.Sprintf("containers nested more than %d deep", maxDepth))
				continue
			}
			n := &Node{Name: strings.ToLower(words[0]), Orig: words[0], Args: words[1:], Quoted: quoted[1:], Line: start, Container: true}
			add(n)
			stack = append(stack, frame{n})
			continue
		}

		words, quoted, err := splitWords(line)
		if err != "" {
			p.problem(start, err)
			continue
		}
		add(&Node{Name: strings.ToLower(words[0]), Orig: words[0], Args: words[1:], Quoted: quoted[1:], Line: start})
	}
	for _, f := range stack {
		p.problem(f.node.Line, fmt.Sprintf("<%s> is never closed", f.node.Name))
	}
	return top, p.problems
}

type parser struct {
	problems []Problem
	count    int
}

func (p *parser) problem(line int, msg string) {
	if len(p.problems) < 50 {
		p.problems = append(p.problems, Problem{line, msg})
	}
}

// splitWords splits a logical line into words the way Apache does: words are
// separated by white space; a word may be quoted with ' or "; inside a word a
// backslash before a backslash (or before the word's own quote) stands for that
// character, and any other backslash is kept (so regular expressions such as
// "\.php$" arrive intact).
func splitWords(s string) (words []string, quoted []bool, problem string) {
	i := 0
	for {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}
		var quote byte
		if s[i] == '"' || s[i] == '\'' {
			quote = s[i]
			i++
		}
		var b strings.Builder
		closed := quote == 0
		for i < len(s) {
			c := s[i]
			if c == '\\' && i+1 < len(s) && (s[i+1] == '\\' || (quote != 0 && s[i+1] == quote)) {
				b.WriteByte(s[i+1])
				i += 2
				continue
			}
			if quote != 0 && c == quote {
				closed = true
				i++
				break
			}
			if quote == 0 && (c == ' ' || c == '\t') {
				break
			}
			b.WriteByte(c)
			i++
		}
		if !closed {
			return nil, nil, "a quote is never closed"
		}
		words = append(words, b.String())
		quoted = append(quoted, quote != 0)
	}
	if len(words) == 0 {
		return nil, nil, "empty directive"
	}
	return words, quoted, ""
}
