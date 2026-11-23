// Package parser builds a generic syntax tree from LOTUS configuration source.
// The syntax is defined in docs/configuration/grammar.md §4. The parser knows
// nothing about specific directives; see the validator for that.
package parser

import (
	"fmt"
	"time"

	"github.com/laikait/lotus/internal/config/ast"
	"github.com/laikait/lotus/internal/config/diag"
	"github.com/laikait/lotus/internal/config/lexer"
)

// Limits from grammar §2.
const (
	MaxFileSize = 16 << 20 // bytes
	MaxDepth    = 64       // nested blocks, lists and calls
)

// Parse parses one configuration file. It always returns a tree (possibly
// partial). The tree must not be used if any returned diagnostic is an error.
func Parse(file string, src []byte) (*ast.File, []diag.Diagnostic) {
	f := &ast.File{Path: file}
	diags := diag.NewList(diag.DefaultMaxErrors)

	if len(src) > MaxFileSize {
		diags.Error(diag.Pos{File: file, Line: 1, Col: 1}, diag.CodeFileTooLarge,
			fmt.Sprintf("file is %d bytes; the limit is %d", len(src), MaxFileSize))
		return f, diags.Items()
	}

	p := &parser{lex: lexer.New(file, string(src), diags), diags: diags}
	p.advance()
	f.Statements, f.Dangling = p.parseStatements(false)
	return f, diags.Items()
}

type pendingComment struct {
	c        ast.Comment
	trailing bool // on the same line as the token before it
}

type parser struct {
	lex      *lexer.Lexer
	diags    *diag.List
	tok      lexer.Token
	lastLine int // line of the last consumed content token
	comments []pendingComment
	depth    int // nesting of blocks, lists and calls
	blocks   int // nesting of blocks only
}

func isContent(k lexer.Kind) bool {
	return k != lexer.EOF && k != lexer.Newline && k != lexer.Semicolon
}

// advance consumes the current token and loads the next non-comment token.
// Comments are collected for the formatter.
func (p *parser) advance() {
	if isContent(p.tok.Kind) {
		p.lastLine = p.tok.Pos.Line
	}
	for {
		t := p.lex.Next()
		if t.Kind == lexer.Comment {
			p.comments = append(p.comments, pendingComment{
				c:        ast.Comment{Pos: t.Pos, Text: t.Text},
				trailing: t.Pos.Line == p.lastLine,
			})
			continue
		}
		p.tok = t
		return
	}
}

func (p *parser) skipTerminators() {
	for p.tok.Kind == lexer.Newline || p.tok.Kind == lexer.Semicolon {
		p.advance()
	}
}

// ---- comments and spacing ----

// takeLeading removes the pending comments that precede the next statement.
// It also reports whether a blank line separates the statement from the
// previous item.
func (p *parser) takeLeading() (lead []ast.Comment, blank bool) {
	prev := p.lastLine
	for _, pc := range p.comments {
		c := pc.c
		c.BlankBefore = c.Pos.Line-prev > 1
		prev = c.Pos.Line
		lead = append(lead, c)
	}
	p.comments = p.comments[:0]
	return lead, p.tok.Pos.Line-prev > 1
}

func (p *parser) takeTrailing() *ast.Comment {
	if len(p.comments) > 0 && p.comments[0].trailing {
		c := p.comments[0].c
		p.comments = p.comments[1:]
		return &c
	}
	return nil
}

func (p *parser) takeDangling() []ast.Comment {
	lead, _ := p.takeLeading()
	return lead
}

// ---- diagnostics ----

func describe(t lexer.Token) string {
	switch t.Kind {
	case lexer.Ident:
		return fmt.Sprintf("identifier %q", t.Text)
	case lexer.Variable:
		return "variable $" + t.Text
	case lexer.String, lexer.RawString, lexer.Integer, lexer.Size, lexer.Duration, lexer.Operator:
		return fmt.Sprintf("%s %s", t.Kind, t.Text)
	default:
		return t.Kind.String()
	}
}

func (p *parser) errorAt(pos diag.Pos, code, msg string) { p.diags.Error(pos, code, msg) }

func (p *parser) unexpected(expected string) {
	p.errorAt(p.tok.Pos, diag.CodeUnexpectedToken, fmt.Sprintf("unexpected %s; expected %s", describe(p.tok), expected))
}

// sync skips to the end of the current statement: the next newline or ';'
// outside any braces. It never consumes the '}' that closes an enclosing block.
func (p *parser) sync() {
	depth := 0
	for {
		switch p.tok.Kind {
		case lexer.EOF:
			return
		case lexer.Newline, lexer.Semicolon:
			if depth == 0 {
				return
			}
		case lexer.LBrace:
			depth++
		case lexer.RBrace:
			if depth == 0 {
				return
			}
			depth--
		}
		if p.diags.Full() {
			return
		}
		p.advance()
	}
}

// ---- statements ----

func (p *parser) parseStatements(inBlock bool) ([]ast.Statement, []ast.Comment) {
	var stmts []ast.Statement
	for {
		p.skipTerminators()
		if p.diags.Full() {
			return stmts, p.takeDangling()
		}
		switch p.tok.Kind {
		case lexer.EOF:
			return stmts, p.takeDangling()
		case lexer.RBrace:
			if inBlock {
				return stmts, p.takeDangling()
			}
			p.errorAt(p.tok.Pos, diag.CodeUnexpectedToken, "unexpected '}' (no block is open)")
			p.advance()
			continue
		}
		lead, blank := p.takeLeading()
		st := p.parseStatement()
		if st == nil {
			continue
		}
		m := st.Meta()
		m.Leading, m.BlankBefore = lead, blank
		stmts = append(stmts, st)
	}
}

func (p *parser) parseStatement() ast.Statement {
	t := p.tok
	switch t.Kind {
	case lexer.Invalid:
		p.advance()
		p.sync()
		return nil
	case lexer.String, lexer.RawString:
		return p.parsePair()
	case lexer.Ident:
		switch t.Text {
		case "let":
			return p.parseLet()
		case "include":
			return p.parseInclude()
		case "true", "false", "null":
			p.errorAt(t.Pos, diag.CodeReservedWord, fmt.Sprintf("%q is a reserved word and can't be a directive name", t.Text))
			p.advance()
			p.sync()
			return nil
		}
		return p.parseDirective()
	}
	p.unexpected("a directive name")
	if t.Kind != lexer.LBrace { // sync counts the brace itself
		p.advance()
	}
	p.sync()
	return nil
}

// endStatement checks that the statement is followed by a terminator.
// Newlines and ';' are left for the caller; '}' and EOF end it implicitly.
func (p *parser) endStatement(what string) bool {
	switch p.tok.Kind {
	case lexer.Newline, lexer.Semicolon, lexer.RBrace, lexer.EOF:
		return true
	case lexer.Invalid:
		return false
	}
	p.unexpected("end of line, ';' or '}' after " + what)
	return false
}

func (p *parser) parseDirective() ast.Statement {
	name := p.tok
	p.advance()
	d := &ast.Directive{Name: name.Text, Pos: name.Pos}

	args, ok := p.parseArgs()
	d.Args = args
	if !ok {
		p.sync()
		return nil
	}
	what := "the arguments"
	if p.tok.Kind == lexer.LBrace {
		d.Block = p.parseBlock()
		what = "'}'"
	}
	d.Trailing = p.takeTrailing()
	if !p.endStatement(what) {
		p.sync()
	}
	return d
}

func (p *parser) parseBlock() *ast.Block {
	open := p.tok
	if p.depth >= MaxDepth {
		p.errorAt(open.Pos, diag.CodeNestingTooDeep, fmt.Sprintf("blocks are nested more than %d levels deep", MaxDepth))
		p.sync() // skips the whole block
		return nil
	}
	p.depth++
	p.blocks++
	p.advance() // '{'
	for i := range p.comments {
		p.comments[i].trailing = false // comments after '{' belong to the first statement
	}
	stmts, dangling := p.parseStatements(true)
	p.blocks--
	p.depth--

	b := &ast.Block{Open: open.Pos, Statements: stmts, Dangling: dangling}
	if p.tok.Kind == lexer.RBrace {
		b.Close = p.tok.Pos
		p.advance()
	} else {
		p.errorAt(open.Pos, diag.CodeUnclosedBlock, "block is never closed: missing '}'")
	}
	return b
}

func (p *parser) parseArgs() ([]ast.Value, bool) {
	var args []ast.Value
	for {
		switch p.tok.Kind {
		case lexer.Newline, lexer.Semicolon, lexer.LBrace, lexer.RBrace, lexer.EOF:
			return args, true
		case lexer.Operator:
			args = append(args, &ast.Operator{Pos: p.tok.Pos, Op: p.tok.Text})
			p.advance()
		default:
			v := p.parseValue()
			if v == nil {
				return args, false
			}
			args = append(args, v)
		}
	}
}

func (p *parser) parsePair() ast.Statement {
	kt := p.tok
	p.advance()
	key := stringValue(kt)
	if p.tok.Kind != lexer.Operator || p.tok.Text != "=" {
		p.unexpected(`'=' after the map key (a quoted string at the start of a line must be a "key" = value pair)`)
		p.sync()
		return nil
	}
	p.advance()
	v := p.parseValue()
	if v == nil {
		p.sync()
		return nil
	}
	pair := &ast.Pair{Key: key, Value: v}
	pair.Trailing = p.takeTrailing()
	if !p.endStatement("the value") {
		p.sync()
	}
	return pair
}

func (p *parser) parseLet() ast.Statement {
	kw := p.tok
	if p.blocks > 0 {
		p.errorAt(kw.Pos, diag.CodeLetNotTopLevel, "'let' is only allowed at the top level of a file")
	}
	p.advance()
	if p.tok.Kind != lexer.Variable {
		p.unexpected("a variable name like $name after 'let'")
		p.sync()
		return nil
	}
	name := p.tok
	p.advance()
	if p.tok.Kind != lexer.Operator || p.tok.Text != "=" {
		p.unexpected("'=' after the variable name")
		p.sync()
		return nil
	}
	p.advance()
	v := p.parseValue()
	if v == nil {
		p.sync()
		return nil
	}
	let := &ast.Let{Pos: kw.Pos, Name: name.Text, NamePos: name.Pos, Value: v}
	let.Trailing = p.takeTrailing()
	if !p.endStatement("the value") {
		p.sync()
	}
	return let
}

func (p *parser) parseInclude() ast.Statement {
	kw := p.tok
	p.advance()
	if p.tok.Kind != lexer.String && p.tok.Kind != lexer.RawString {
		p.unexpected(`a quoted path after 'include'`)
		p.sync()
		return nil
	}
	path := stringValue(p.tok)
	p.advance()
	inc := &ast.Include{Pos: kw.Pos, Path: path}
	inc.Trailing = p.takeTrailing()
	if !p.endStatement("the include path") {
		p.sync()
	}
	return inc
}

// ---- values ----

func stringValue(t lexer.Token) *ast.String {
	return &ast.String{Pos: t.Pos, Raw: t.Kind == lexer.RawString, Parts: t.Parts, Source: t.Text}
}

// parseValue parses one value. It returns nil after reporting an error; the
// caller then resynchronizes. Tokens with lexical errors yield ast.Bad and no
// further diagnostics.
func (p *parser) parseValue() ast.Value {
	t := p.tok
	switch t.Kind {
	case lexer.Invalid:
		p.advance()
		return &ast.Bad{Pos: t.Pos}
	case lexer.String, lexer.RawString:
		p.advance()
		return stringValue(t)
	case lexer.Integer:
		p.advance()
		return &ast.Int{Pos: t.Pos, Value: t.Num, Source: t.Text}
	case lexer.Size:
		p.advance()
		return &ast.Size{Pos: t.Pos, Bytes: t.Num, Source: t.Text}
	case lexer.Duration:
		p.advance()
		return &ast.Duration{Pos: t.Pos, Value: time.Duration(t.Num), Source: t.Text}
	case lexer.Variable:
		p.advance()
		return &ast.Variable{Pos: t.Pos, Name: t.Text}
	case lexer.LBracket:
		return p.parseList()
	case lexer.Ident:
		switch t.Text {
		case "true", "false":
			p.advance()
			return &ast.Bool{Pos: t.Pos, Value: t.Text == "true"}
		case "null":
			p.advance()
			return &ast.Null{Pos: t.Pos}
		case "let", "include":
			p.errorAt(t.Pos, diag.CodeReservedWord, fmt.Sprintf("%q is a reserved word and can't be used as a value", t.Text))
			return nil
		}
		if t.Adjacent {
			return p.parseCall()
		}
		p.advance()
		return &ast.Ident{Pos: t.Pos, Name: t.Text}
	}
	p.unexpected("a value")
	return nil
}

func (p *parser) parseList() ast.Value {
	open := p.tok
	if !p.enter(open.Pos) {
		return nil
	}
	defer p.leave()
	p.advance() // '['
	items, ok := p.parseSequence(lexer.RBracket, open.Pos, "list", "']'")
	if !ok {
		return nil
	}
	return &ast.List{Pos: open.Pos, Items: items}
}

func (p *parser) parseCall() ast.Value {
	name := p.tok
	if !p.enter(name.Pos) {
		return nil
	}
	defer p.leave()
	p.advance() // name; the current token is now '('
	open := p.tok
	p.advance() // '('
	args, ok := p.parseSequence(lexer.RParen, open.Pos, "argument list", "')'")
	if !ok {
		return nil
	}
	return &ast.Call{Pos: name.Pos, Name: name.Text, Args: args}
}

func (p *parser) enter(pos diag.Pos) bool {
	if p.depth >= MaxDepth {
		p.errorAt(pos, diag.CodeNestingTooDeep, fmt.Sprintf("values are nested more than %d levels deep", MaxDepth))
		return false
	}
	p.depth++
	return true
}

func (p *parser) leave() { p.depth-- }

// parseSequence parses comma-separated values up to closer (after the opener
// has been consumed). A trailing comma is allowed.
func (p *parser) parseSequence(closer lexer.Kind, open diag.Pos, what, closeText string) ([]ast.Value, bool) {
	var items []ast.Value
	for {
		switch p.tok.Kind {
		case closer:
			p.advance()
			return items, true
		case lexer.EOF:
			p.errorAt(open, diag.CodeUnclosedList, fmt.Sprintf("%s is never closed: missing %s", what, closeText))
			return items, false
		}
		v := p.parseValue()
		if v == nil {
			return items, false
		}
		items = append(items, v)
		switch p.tok.Kind {
		case lexer.Comma:
			p.advance()
		case closer:
			// loop closes it
		case lexer.EOF:
			p.errorAt(open, diag.CodeUnclosedList, fmt.Sprintf("%s is never closed: missing %s", what, closeText))
			return items, false
		default:
			if p.tok.Kind != lexer.Invalid {
				p.unexpected("',' or " + closeText)
			}
			return items, false
		}
	}
}
