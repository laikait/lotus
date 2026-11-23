// Package ast defines the generic syntax tree produced by the configuration
// parser. The tree knows nothing about specific directives; the validator
// checks names, contexts and argument types against the directive registry.
package ast

import (
	"strings"
	"time"

	"github.com/laikait/lotus/internal/config/diag"
)

// Node is anything with a source position.
type Node interface{ Position() diag.Pos }

// Comment is a `# ...` comment. Text includes the leading '#'.
type Comment struct {
	Pos         diag.Pos
	Text        string
	BlankBefore bool // a blank line separates it from what came before
}

// File is a parsed configuration file.
type File struct {
	Path       string
	Statements []Statement
	Dangling   []Comment // comments after the last statement
}

// Statement is a directive, let, include or map pair.
type Statement interface {
	Node
	Meta() *StmtMeta
}

// StmtMeta holds the comments and spacing that belong to a statement.
// The formatter uses it to keep comments and blank lines.
type StmtMeta struct {
	Leading     []Comment // own-line comments directly above the statement
	Trailing    *Comment  // comment on the same line after the statement
	BlankBefore bool      // blank line between the previous item and this one
}

// Meta returns the statement metadata.
func (m *StmtMeta) Meta() *StmtMeta { return m }

// Directive is `name arg... [ { block } ]`.
type Directive struct {
	StmtMeta
	Pos   diag.Pos
	Name  string
	Args  []Value
	Block *Block
}

// Block is a `{ ... }` body.
type Block struct {
	Open, Close diag.Pos
	Statements  []Statement
	Dangling    []Comment // comments before the closing brace
}

// Let is `let $name = value`.
type Let struct {
	StmtMeta
	Pos     diag.Pos
	Name    string // without '$'
	NamePos diag.Pos
	Value   Value
}

// Include is `include "path"`.
type Include struct {
	StmtMeta
	Pos  diag.Pos
	Path *String
}

// Pair is a map entry: `"key" = value`.
type Pair struct {
	StmtMeta
	Key   *String
	Value Value
}

// Position implementations for statements.
func (d *Directive) Position() diag.Pos { return d.Pos }
func (l *Let) Position() diag.Pos       { return l.Pos }
func (i *Include) Position() diag.Pos   { return i.Pos }
func (p *Pair) Position() diag.Pos      { return p.Key.Pos }

// Value is an argument or a right-hand side.
type Value interface {
	Node
	value()
}

// PartKind distinguishes the pieces of an interpolated string.
type PartKind int

// Part kinds.
const (
	PartText PartKind = iota // literal text
	PartVar                  // ${name}
)

// StringPart is a piece of a string literal.
type StringPart struct {
	Kind PartKind
	Text string // PartText: decoded text
	Var  string // PartVar: variable name
	// Escaped marks text that is literal and must never be scanned for
	// request-time placeholders: a '{' or '}' written as \{ or \}, and any
	// text that came from env() or file().
	Escaped bool
}

// String is a double-quoted (Raw == false) or single-quoted (Raw == true) string.
type String struct {
	Pos    diag.Pos
	Raw    bool
	Parts  []StringPart
	Source string // text as written, with quotes
	// Secret is set by the loader for values that came from env() or file()
	// (or were built from them). Secret values never appear in diagnostics,
	// dumps or inspect output.
	Secret bool
}

// Plain returns the string's text and true when it has no ${...} parts.
func (s *String) Plain() (string, bool) {
	var b strings.Builder
	for _, p := range s.Parts {
		if p.Kind == PartVar {
			return "", false
		}
		b.WriteString(p.Text)
	}
	return b.String(), true
}

// Int is an integer literal.
type Int struct {
	Pos    diag.Pos
	Value  int64
	Source string
}

// Size is a size literal such as 100mb; Bytes is the value in bytes.
type Size struct {
	Pos    diag.Pos
	Bytes  int64
	Source string
}

// Duration is a duration literal such as 1h30m.
type Duration struct {
	Pos    diag.Pos
	Value  time.Duration
	Source string
}

// Bool is true or false.
type Bool struct {
	Pos   diag.Pos
	Value bool
}

// Null is the null literal.
type Null struct{ Pos diag.Pos }

// Ident is an enum word such as `auto` or `https`.
type Ident struct {
	Pos  diag.Pos
	Name string
}

// Variable is a `$name` reference.
type Variable struct {
	Pos  diag.Pos
	Name string
}

// List is `[a, b, c]`.
type List struct {
	Pos   diag.Pos
	Items []Value
}

// Call is a built-in function call such as env("NAME").
type Call struct {
	Pos  diag.Pos
	Name string
	Args []Value
}

// Operator is one of = ~ ~* ^~ !~ !~* used as a directive argument.
type Operator struct {
	Pos diag.Pos
	Op  string
}

// Bad stands in for a value that had a lexical error. A diagnostic has
// already been reported for it.
type Bad struct{ Pos diag.Pos }

// Position implementations for values.
func (v *String) Position() diag.Pos   { return v.Pos }
func (v *Int) Position() diag.Pos      { return v.Pos }
func (v *Size) Position() diag.Pos     { return v.Pos }
func (v *Duration) Position() diag.Pos { return v.Pos }
func (v *Bool) Position() diag.Pos     { return v.Pos }
func (v *Null) Position() diag.Pos     { return v.Pos }
func (v *Ident) Position() diag.Pos    { return v.Pos }
func (v *Variable) Position() diag.Pos { return v.Pos }
func (v *List) Position() diag.Pos     { return v.Pos }
func (v *Call) Position() diag.Pos     { return v.Pos }
func (v *Operator) Position() diag.Pos { return v.Pos }
func (v *Bad) Position() diag.Pos      { return v.Pos }

func (*String) value()   {}
func (*Int) value()      {}
func (*Size) value()     {}
func (*Duration) value() {}
func (*Bool) value()     {}
func (*Null) value()     {}
func (*Ident) value()    {}
func (*Variable) value() {}
func (*List) value()     {}
func (*Call) value()     {}
func (*Operator) value() {}
func (*Bad) value()      {}
