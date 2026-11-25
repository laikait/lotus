// Package format prints a configuration file in the canonical style of
// docs/configuration/grammar.md §8: 4-space indentation, one directive per
// line, long lists broken one item per line, comments and single blank lines
// kept. Formatting never changes meaning and is idempotent.
package format

import (
	"errors"
	"fmt"
	"strings"

	"github.com/laikait/lotus/internal/config/ast"
	"github.com/laikait/lotus/internal/config/diag"
	"github.com/laikait/lotus/internal/config/parser"
)

// MaxLine is the column limit above which a list is split over several lines.
const MaxLine = 100

// ErrSyntax means the source has errors, so it can't be formatted.
var ErrSyntax = errors.New("the file has syntax errors")

// Source formats configuration text. On syntax errors it returns them and
// ErrSyntax.
func Source(name string, src []byte) ([]byte, []diag.Diagnostic, error) {
	f, diags := parser.Parse(name, src)
	for _, d := range diags {
		if d.Severity == diag.Error {
			return nil, diags, ErrSyntax
		}
	}
	out, err := File(f)
	return out, diags, err
}

// File prints a parsed file.
func File(f *ast.File) ([]byte, error) {
	p := &printer{}
	p.statements(f.Statements, f.Dangling, 0)
	if p.err != nil {
		return nil, p.err
	}
	return []byte(p.b.String()), nil
}

type printer struct {
	b   strings.Builder
	err error
}

func (p *printer) raw(s string) { p.b.WriteString(s) }

func (p *printer) blank() {
	// Never at the start of the output, and never two in a row.
	s := p.b.String()
	if s == "" || strings.HasSuffix(s, "\n\n") {
		return
	}
	p.b.WriteByte('\n')
}

func indent(depth int) string { return strings.Repeat("    ", depth) }

func commentText(c ast.Comment) string { return strings.TrimRight(c.Text, " \t\r") }

// statements prints the statements of a file or block and the comments left
// at its end. In a block, the first item gets no blank line before it.
func (p *printer) statements(stmts []ast.Statement, dangling []ast.Comment, depth int) {
	first := true
	for _, s := range stmts {
		meta := s.Meta()
		for i, c := range meta.Leading {
			if c.BlankBefore && !first {
				p.blank()
			}
			p.raw(indent(depth) + commentText(c) + "\n")
			first = false
			_ = i
		}
		if meta.BlankBefore && !first {
			p.blank()
		}
		p.statement(s, depth)
		first = false
	}
	for _, c := range dangling {
		if c.BlankBefore && !first {
			p.blank()
		}
		p.raw(indent(depth) + commentText(c) + "\n")
		first = false
	}
}

func trailing(m *ast.StmtMeta) string {
	if m.Trailing == nil {
		return ""
	}
	return " " + commentText(*m.Trailing)
}

func (p *printer) statement(s ast.Statement, depth int) {
	ind := indent(depth)
	switch x := s.(type) {
	case *ast.Let:
		p.raw(ind + p.assign("let $"+x.Name+" = ", x.Value, depth) + trailing(&x.StmtMeta) + "\n")
	case *ast.Include:
		p.raw(ind + "include " + x.Path.Source + trailing(&x.StmtMeta) + "\n")
	case *ast.Pair:
		p.raw(ind + p.assign(x.Key.Source+" = ", x.Value, depth) + trailing(&x.StmtMeta) + "\n")
	case *ast.Directive:
		p.directive(x, depth)
	default:
		p.err = fmt.Errorf("unknown statement %T", s)
	}
}

// assign prints "prefix value", splitting a list when the line is too long.
func (p *printer) assign(prefix string, v ast.Value, depth int) string {
	one := prefix + p.value(v, depth, false)
	if len(indent(depth))+len(one) <= MaxLine {
		return one
	}
	return prefix + p.value(v, depth, true)
}

func (p *printer) directive(d *ast.Directive, depth int) {
	ind := indent(depth)
	// First try everything on one line.
	one := d.Name
	for _, a := range d.Args {
		one += " " + p.value(a, depth, false)
	}
	if d.Block != nil {
		one += " {"
	}
	multi := len(ind)+len(one) > MaxLine
	head := one
	if multi {
		head = d.Name
		for _, a := range d.Args {
			head += " " + p.value(a, depth, true)
		}
		if d.Block != nil {
			head += " {"
		}
	}
	if d.Block == nil {
		p.raw(ind + head + trailing(&d.StmtMeta) + "\n")
		return
	}
	p.raw(ind + head + "\n")
	p.statements(d.Block.Statements, d.Block.Dangling, depth+1)
	p.raw(ind + "}" + trailing(&d.StmtMeta) + "\n")
}

// value prints a value. With split, a list that is part of a too-long line
// is written one item per line.
func (p *printer) value(v ast.Value, depth int, split bool) string {
	switch x := v.(type) {
	case *ast.String:
		return x.Source
	case *ast.Int:
		return x.Source
	case *ast.Size:
		return x.Source
	case *ast.Duration:
		return x.Source
	case *ast.Bool:
		if x.Value {
			return "true"
		}
		return "false"
	case *ast.Null:
		return "null"
	case *ast.Ident:
		return x.Name
	case *ast.Variable:
		return "$" + x.Name
	case *ast.Operator:
		return x.Op
	case *ast.Call:
		parts := make([]string, len(x.Args))
		for i, a := range x.Args {
			parts[i] = p.value(a, depth, false)
		}
		return x.Name + "(" + strings.Join(parts, ", ") + ")"
	case *ast.List:
		if len(x.Items) == 0 {
			return "[]"
		}
		items := make([]string, len(x.Items))
		for i, it := range x.Items {
			items[i] = p.value(it, depth, false)
		}
		if !split {
			return "[ " + strings.Join(items, ", ") + " ]"
		}
		var b strings.Builder
		b.WriteString("[\n")
		for _, it := range items {
			b.WriteString(indent(depth+1) + it + ",\n")
		}
		b.WriteString(indent(depth) + "]")
		return b.String()
	}
	p.err = fmt.Errorf("cannot format %T", v)
	return ""
}
