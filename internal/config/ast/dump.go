package ast

import (
	"fmt"
	"strings"
)

// Dump renders the tree as indented text, one node per line, with positions.
// It is deterministic and is used for golden-file tests.
func Dump(f *File) string {
	var d dumper
	d.line(0, "File")
	d.statements(1, f.Statements)
	d.comments(1, "dangling", f.Dangling)
	return d.b.String()
}

type dumper struct{ b strings.Builder }

func (d *dumper) line(depth int, format string, args ...any) {
	d.b.WriteString(strings.Repeat("  ", depth))
	fmt.Fprintf(&d.b, format, args...)
	d.b.WriteByte('\n')
}

func (d *dumper) comments(depth int, label string, cs []Comment) {
	for _, c := range cs {
		blank := ""
		if c.BlankBefore {
			blank = " [blank]"
		}
		d.line(depth, "%s %q @%d:%d%s", label, c.Text, c.Pos.Line, c.Pos.Col, blank)
	}
}

func (d *dumper) statements(depth int, stmts []Statement) {
	for _, s := range stmts {
		m := s.Meta()
		d.comments(depth, "leading", m.Leading)
		blank := ""
		if m.BlankBefore {
			blank = " [blank]"
		}
		switch s := s.(type) {
		case *Directive:
			d.line(depth, "Directive %q @%d:%d%s", s.Name, s.Pos.Line, s.Pos.Col, blank)
			d.values(depth+1, s.Args)
			if s.Block != nil {
				d.line(depth+1, "Block @%d:%d-%d:%d", s.Block.Open.Line, s.Block.Open.Col, s.Block.Close.Line, s.Block.Close.Col)
				d.statements(depth+2, s.Block.Statements)
				d.comments(depth+2, "dangling", s.Block.Dangling)
			}
		case *Let:
			d.line(depth, "Let $%s @%d:%d%s", s.Name, s.Pos.Line, s.Pos.Col, blank)
			d.value(depth+1, s.Value)
		case *Include:
			d.line(depth, "Include @%d:%d%s", s.Pos.Line, s.Pos.Col, blank)
			d.value(depth+1, s.Path)
		case *Pair:
			d.line(depth, "Pair @%d:%d%s", s.Key.Pos.Line, s.Key.Pos.Col, blank)
			d.value(depth+1, s.Key)
			d.value(depth+1, s.Value)
		}
		if m.Trailing != nil {
			d.comments(depth+1, "trailing", []Comment{*m.Trailing})
		}
	}
}

func (d *dumper) values(depth int, vs []Value) {
	for _, v := range vs {
		d.value(depth, v)
	}
}

func (d *dumper) value(depth int, v Value) {
	p := v.Position()
	at := fmt.Sprintf("@%d:%d", p.Line, p.Col)
	switch v := v.(type) {
	case *String:
		kind := "String"
		if v.Raw {
			kind = "RawString"
		}
		if v.Secret {
			d.line(depth, "%s %s [secret redacted]", kind, at)
			break
		}
		var parts []string
		for _, pt := range v.Parts {
			switch {
			case pt.Kind == PartVar:
				parts = append(parts, "var:"+pt.Var)
			case pt.Escaped:
				parts = append(parts, fmt.Sprintf("esc:%q", pt.Text))
			default:
				parts = append(parts, fmt.Sprintf("text:%q", pt.Text))
			}
		}
		d.line(depth, "%s %s [%s]", kind, at, strings.Join(parts, " "))
	case *Int:
		d.line(depth, "Int %d %q %s", v.Value, v.Source, at)
	case *Size:
		d.line(depth, "Size %d %q %s", v.Bytes, v.Source, at)
	case *Duration:
		d.line(depth, "Duration %d %q %s", int64(v.Value), v.Source, at)
	case *Bool:
		d.line(depth, "Bool %t %s", v.Value, at)
	case *Null:
		d.line(depth, "Null %s", at)
	case *Ident:
		d.line(depth, "Ident %s %s", v.Name, at)
	case *Variable:
		d.line(depth, "Variable $%s %s", v.Name, at)
	case *Operator:
		d.line(depth, "Operator %s %s", v.Op, at)
	case *List:
		d.line(depth, "List %s", at)
		d.values(depth+1, v.Items)
	case *Call:
		d.line(depth, "Call %s %s", v.Name, at)
		d.values(depth+1, v.Args)
	case *Bad:
		d.line(depth, "Bad %s", at)
	}
}
