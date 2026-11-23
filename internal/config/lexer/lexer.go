// Package lexer turns configuration source text into tokens.
// The rules are defined in docs/configuration/grammar.md §3.
package lexer

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/laikait/lotus/internal/config/ast"
	"github.com/laikait/lotus/internal/config/diag"
)

// Kind is the type of a token.
type Kind int

// Token kinds.
const (
	EOF     Kind = iota
	Invalid      // a token with a lexical error; a diagnostic was already reported
	Newline
	Semicolon
	Comment
	Ident
	Variable
	String
	RawString
	Integer
	Size
	Duration
	Operator
	LBrace
	RBrace
	LBracket
	RBracket
	LParen
	RParen
	Comma
)

var kindNames = [...]string{
	EOF: "end of file", Invalid: "invalid token", Newline: "end of line", Semicolon: "';'",
	Comment: "comment", Ident: "identifier", Variable: "variable", String: "string",
	RawString: "raw string", Integer: "integer", Size: "size", Duration: "duration",
	Operator: "operator", LBrace: "'{'", RBrace: "'}'", LBracket: "'['", RBracket: "']'",
	LParen: "'('", RParen: "')'", Comma: "','",
}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "token"
}

// Token is one lexical element.
type Token struct {
	Kind  Kind
	Pos   diag.Pos
	Text  string           // source text; for Variable the name without '$'
	Parts []ast.StringPart // String and RawString
	Num   int64            // Integer value, Size in bytes, Duration in nanoseconds
	// Adjacent is set on an Ident that is immediately followed by '(' (a call).
	Adjacent bool
}

// utf8BOM is the UTF-8 byte order mark (U+FEFF), written as bytes.
const utf8BOM = "\xef\xbb\xbf"

// Lexer produces tokens from a source string.
type Lexer struct {
	file  string
	src   string
	off   int
	line  int
	col   int
	depth int // nesting of [] and (); newlines inside are not tokens
	diags *diag.List
}

// New creates a lexer. Problems are reported to diags.
func New(file, src string, diags *diag.List) *Lexer {
	l := &Lexer{file: file, src: src, line: 1, col: 1, diags: diags}
	if strings.HasPrefix(src, utf8BOM) {
		l.off = len(utf8BOM) // a BOM is ignored and doesn't count as a column
	}
	if !utf8.ValidString(src) {
		l.reportInvalidUTF8()
		l.off = len(src) // nothing else is lexed
	}
	return l
}

func (l *Lexer) reportInvalidUTF8() {
	line, col := 1, 1
	for i := l.off; i < len(l.src); {
		r, size := utf8.DecodeRuneInString(l.src[i:])
		if r == utf8.RuneError && size == 1 {
			l.diags.Error(diag.Pos{File: l.file, Offset: i, Line: line, Col: col},
				diag.CodeInvalidUTF8, "file is not valid UTF-8")
			return
		}
		if r == '\n' {
			line++
			col = 1
		} else {
			col++
		}
		i += size
	}
}

func (l *Lexer) pos() diag.Pos {
	return diag.Pos{File: l.file, Offset: l.off, Line: l.line, Col: l.col}
}

func (l *Lexer) peek() (rune, int) {
	if l.off >= len(l.src) {
		return -1, 0
	}
	return utf8.DecodeRuneInString(l.src[l.off:])
}

func (l *Lexer) advance(r rune, size int) {
	l.off += size
	if r == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
}

func (l *Lexer) errorf(pos diag.Pos, code, format string, args ...any) {
	l.diags.Error(pos, code, fmt.Sprintf(format, args...))
}

func isDigit(r rune) bool      { return r >= '0' && r <= '9' }
func isLetter(r rune) bool     { return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' }
func isIdentStart(r rune) bool { return isLetter(r) || r == '_' }
func isIdentChar(r rune) bool  { return isIdentStart(r) || isDigit(r) }
func isControl(r rune) bool    { return r < 0x20 && r != '\t' || r == 0x7f }
func hasUpper(s string) bool   { return strings.ToLower(s) != s }
func (l *Lexer) token(k Kind, start diag.Pos) Token {
	return Token{Kind: k, Pos: start, Text: l.src[start.Offset:l.off]}
}

// Next returns the next token. After the end of input (or when the error limit
// is reached) it returns EOF forever.
func (l *Lexer) Next() Token {
	for {
		if l.diags.Full() {
			return Token{Kind: EOF, Pos: l.pos()}
		}
		r, size := l.peek()
		if size == 0 {
			return Token{Kind: EOF, Pos: l.pos()}
		}
		start := l.pos()
		switch {
		case r == ' ' || r == '\t':
			l.advance(r, size)
		case r == '\r':
			l.advance(r, size)
			if n, ns := l.peek(); n == '\n' {
				l.advance(n, ns)
				if l.depth == 0 {
					return Token{Kind: Newline, Pos: start, Text: "\r\n"}
				}
				continue
			}
			l.errorf(start, diag.CodeUnexpectedChar, "unexpected character %q (a bare carriage return is not allowed)", r)
		case r == '\n':
			l.advance(r, size)
			if l.depth == 0 {
				return Token{Kind: Newline, Pos: start, Text: "\n"}
			}
		case r == '#':
			for {
				c, cs := l.peek()
				if cs == 0 || c == '\n' || c == '\r' {
					break
				}
				l.advance(c, cs)
			}
			return l.token(Comment, start)
		case r == ';':
			l.advance(r, size)
			return l.token(Semicolon, start)
		case r == '{':
			l.advance(r, size)
			return l.token(LBrace, start)
		case r == '}':
			l.advance(r, size)
			return l.token(RBrace, start)
		case r == '[' || r == '(':
			l.advance(r, size)
			l.depth++
			if r == '[' {
				return l.token(LBracket, start)
			}
			return l.token(LParen, start)
		case r == ']' || r == ')':
			l.advance(r, size)
			if l.depth > 0 {
				l.depth--
			}
			if r == ']' {
				return l.token(RBracket, start)
			}
			return l.token(RParen, start)
		case r == ',':
			l.advance(r, size)
			return l.token(Comma, start)
		case r == '"':
			return l.lexString(start)
		case r == '\'':
			return l.lexRawString(start)
		case r == '$':
			return l.lexVariable(start)
		case r == '=' || r == '~' || r == '^' || r == '!':
			if t, ok := l.lexOperator(start); ok {
				return t
			}
		case isDigit(r):
			return l.lexNumber(start)
		case isIdentStart(r):
			return l.lexIdent(start)
		default:
			l.advance(r, size)
			l.errorf(start, diag.CodeUnexpectedChar, "unexpected character %q", r)
		}
	}
}

func (l *Lexer) lexOperator(start diag.Pos) (Token, bool) {
	r, size := l.peek()
	l.advance(r, size)
	n, ns := l.peek()
	switch r {
	case '=':
		return l.token(Operator, start), true
	case '~':
		if n == '*' {
			l.advance(n, ns)
		}
		return l.token(Operator, start), true
	case '^':
		if n == '~' {
			l.advance(n, ns)
			return l.token(Operator, start), true
		}
		l.errorf(start, diag.CodeUnexpectedChar, "unexpected character '^' (did you mean '^~'?)")
	case '!':
		if n == '~' {
			l.advance(n, ns)
			if m, ms := l.peek(); m == '*' {
				l.advance(m, ms)
			}
			return l.token(Operator, start), true
		}
		l.errorf(start, diag.CodeUnexpectedChar, "unexpected character '!' (did you mean '!~' or '!~*'?)")
	}
	return Token{}, false
}

func (l *Lexer) scanIdentChars() string {
	begin := l.off
	for {
		r, size := l.peek()
		if size == 0 || !isIdentChar(r) {
			return l.src[begin:l.off]
		}
		l.advance(r, size)
	}
}

func (l *Lexer) lexIdent(start diag.Pos) Token {
	name := l.scanIdentChars()
	if hasUpper(name) {
		l.diags.ErrorHint(start, diag.CodeInvalidIdent,
			fmt.Sprintf("identifier %q must be lowercase", name), "use "+strings.ToLower(name))
		return Token{Kind: Invalid, Pos: start, Text: name}
	}
	t := Token{Kind: Ident, Pos: start, Text: name}
	t.Adjacent = l.off < len(l.src) && l.src[l.off] == '('
	return t
}

func (l *Lexer) lexVariable(start diag.Pos) Token {
	l.advance('$', 1)
	if r, size := l.peek(); size == 0 || !isIdentStart(r) {
		l.errorf(start, diag.CodeUnexpectedChar, "expected a variable name after '$'")
		return Token{Kind: Invalid, Pos: start, Text: "$"}
	}
	name := l.scanIdentChars()
	if hasUpper(name) {
		l.diags.ErrorHint(start, diag.CodeInvalidIdent,
			fmt.Sprintf("variable name %q must be lowercase", name), "use $"+strings.ToLower(name))
		return Token{Kind: Invalid, Pos: start, Text: name}
	}
	return Token{Kind: Variable, Pos: start, Text: name}
}

// ---- strings ----

func (l *Lexer) lexString(start diag.Pos) Token {
	l.advance('"', 1)
	var parts []ast.StringPart
	var sb strings.Builder
	flush := func() {
		if sb.Len() > 0 {
			parts = append(parts, ast.StringPart{Kind: ast.PartText, Text: sb.String()})
			sb.Reset()
		}
	}
	bad := false
	for {
		r, size := l.peek()
		switch {
		case size == 0 || r == '\n' || r == '\r':
			l.errorf(start, diag.CodeUnterminatedString, "string is not closed (strings can't span lines)")
			return Token{Kind: Invalid, Pos: start}
		case r == '"':
			l.advance(r, size)
			if bad {
				return Token{Kind: Invalid, Pos: start}
			}
			flush()
			t := l.token(String, start)
			t.Parts = parts
			return t
		case r == '\\':
			escPos := l.pos()
			l.advance(r, size)
			e, es := l.peek()
			switch e {
			case '"', '\\', '$':
				sb.WriteRune(e)
				l.advance(e, es)
			case 'n':
				sb.WriteByte('\n')
				l.advance(e, es)
			case 't':
				sb.WriteByte('\t')
				l.advance(e, es)
			case 'r':
				sb.WriteByte('\r')
				l.advance(e, es)
			case '{', '}':
				flush()
				parts = append(parts, ast.StringPart{Kind: ast.PartText, Text: string(e), Escaped: true})
				l.advance(e, es)
			case 'u':
				l.advance(e, es)
				if v, ok := l.lexUnicodeEscape(escPos); ok {
					sb.WriteRune(v)
				} else {
					bad = true
				}
			default:
				bad = true
				if es == 0 || e == '\n' || e == '\r' {
					l.errorf(escPos, diag.CodeInvalidEscape, "invalid escape: backslash at end of line")
				} else {
					l.diags.ErrorHint(escPos, diag.CodeInvalidEscape,
						fmt.Sprintf("invalid escape sequence \\%c", e),
						`valid escapes: \" \\ \n \t \r \$ \{ \} \u{hex}; use a single-quoted string to keep backslashes`)
					l.advance(e, es)
				}
			}
		case r == '$':
			if l.off+1 < len(l.src) && l.src[l.off+1] == '{' {
				varPos := l.pos()
				l.advance('$', 1)
				l.advance('{', 1)
				name := l.scanIdentChars()
				if c, cs := l.peek(); c == '}' && name != "" && !isDigit(rune(name[0])) && !hasUpper(name) {
					l.advance(c, cs)
					flush()
					parts = append(parts, ast.StringPart{Kind: ast.PartVar, Var: name})
				} else {
					bad = true
					l.diags.ErrorHint(varPos, diag.CodeBadInterpolation,
						"invalid ${...} interpolation: expected ${name} with a lowercase variable name",
						`write \${ for a literal "${"`)
					if c == '}' {
						l.advance(c, cs)
					}
				}
			} else {
				sb.WriteByte('$')
				l.advance(r, size)
			}
		case isControl(r):
			bad = true
			l.errorf(l.pos(), diag.CodeUnexpectedChar, "control character %q is not allowed in a string (use an escape)", r)
			l.advance(r, size)
		default:
			sb.WriteRune(r)
			l.advance(r, size)
		}
	}
}

func (l *Lexer) lexUnicodeEscape(escPos diag.Pos) (rune, bool) {
	fail := func(msg string) (rune, bool) {
		l.errorf(escPos, diag.CodeInvalidEscape, "%s", msg)
		return 0, false
	}
	if c, cs := l.peek(); c != '{' {
		return fail(`invalid escape: \u must be followed by {hex}`)
	} else {
		l.advance(c, cs)
	}
	begin := l.off
	for {
		c, cs := l.peek()
		if cs == 0 || c == '"' || c == '\n' || c == '\r' {
			return fail(`invalid escape: missing '}' in \u{...}`)
		}
		if c == '}' {
			digits := l.src[begin:l.off]
			l.advance(c, cs)
			if digits == "" || len(digits) > 6 {
				return fail(`invalid escape: \u{...} needs 1 to 6 hex digits`)
			}
			v, err := strconv.ParseUint(digits, 16, 32)
			if err != nil || v > utf8.MaxRune || (v >= 0xD800 && v <= 0xDFFF) {
				return fail(`invalid escape: not a valid Unicode scalar value`)
			}
			if isControl(rune(v)) {
				return fail(`invalid escape: control characters are not allowed (use \n, \t or \r)`)
			}
			return rune(v), true
		}
		l.advance(c, cs)
	}
}

func (l *Lexer) lexRawString(start diag.Pos) Token {
	l.advance('\'', 1)
	begin := l.off
	bad := false
	for {
		r, size := l.peek()
		switch {
		case size == 0 || r == '\n' || r == '\r':
			l.errorf(start, diag.CodeUnterminatedString, "raw string is not closed (strings can't span lines)")
			return Token{Kind: Invalid, Pos: start}
		case r == '\'':
			text := l.src[begin:l.off]
			l.advance(r, size)
			if bad {
				return Token{Kind: Invalid, Pos: start}
			}
			t := l.token(RawString, start)
			t.Parts = []ast.StringPart{{Kind: ast.PartText, Text: text}}
			return t
		case isControl(r):
			bad = true
			l.errorf(l.pos(), diag.CodeUnexpectedChar, "control character %q is not allowed in a string", r)
			l.advance(r, size)
		default:
			l.advance(r, size)
		}
	}
}

// ---- numbers, sizes, durations ----

var sizeUnits = map[string]int64{
	"b": 1, "kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12,
	"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40,
}

type durUnit struct {
	ns   int64
	rank int // larger unit = larger rank
}

var durUnits = map[string]durUnit{
	"d": {24 * 3600e9, 5}, "h": {3600e9, 4}, "m": {60e9, 3}, "s": {1e9, 2}, "ms": {1e6, 1},
}

func (l *Lexer) lexNumber(start diag.Pos) Token {
	begin := l.off
	for {
		r, size := l.peek()
		if size == 0 || !(isDigit(r) || isLetter(r) || r == '_') {
			break
		}
		l.advance(r, size)
	}
	text := l.src[begin:l.off]
	kind, val, code, msg, hint := classifyNumber(text)
	if msg != "" {
		l.diags.ErrorHint(start, code, msg, hint)
		return Token{Kind: Invalid, Pos: start, Text: text}
	}
	return Token{Kind: kind, Pos: start, Text: text, Num: val}
}

// scanDigits reads a run of digits and '_' separators from the front of s.
func scanDigits(s string) (digits, rest, errMsg string) {
	i := 0
	for i < len(s) && (isDigit(rune(s[i])) || s[i] == '_') {
		if s[i] == '_' && (i == 0 || !isDigit(rune(s[i-1])) || i+1 >= len(s) || !isDigit(rune(s[i+1]))) {
			return "", s, "'_' may only appear between digits"
		}
		i++
	}
	return strings.ReplaceAll(s[:i], "_", ""), s[i:], ""
}

func classifyNumber(text string) (kind Kind, val int64, code, msg, hint string) {
	rangeErr := func() (Kind, int64, string, string, string) {
		return Invalid, 0, diag.CodeNumberRange, fmt.Sprintf("%q is too large (limit is 64-bit)", text), ""
	}
	numErr := func(m, h string) (Kind, int64, string, string, string) {
		return Invalid, 0, diag.CodeInvalidNumber, m, h
	}

	digits, rest, e := scanDigits(text)
	if e != "" {
		return numErr(fmt.Sprintf("invalid number %q: %s", text, e), "")
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return rangeErr()
	}
	if rest == "" {
		return Integer, n, "", "", ""
	}
	if mult, ok := sizeUnits[rest]; ok {
		if n > math.MaxInt64/mult {
			return rangeErr()
		}
		return Size, n * mult, "", "", ""
	}

	// Duration: one or more <digits><unit> parts, largest unit first.
	var total int64
	lastRank := math.MaxInt
	s := text
	for len(s) > 0 {
		d, r, e := scanDigits(s)
		if e != "" {
			return numErr(fmt.Sprintf("invalid number %q: %s", text, e), "")
		}
		if d == "" {
			return numErr(fmt.Sprintf("invalid number or unit %q", text), "")
		}
		j := 0
		for j < len(r) && isLetter(rune(r[j])) {
			j++
		}
		unit := r[:j]
		if unit == "" {
			return numErr(fmt.Sprintf("number in %q is missing a unit", text), "durations use ms, s, m, h, d; sizes use b, kb, mb, gb, tb, kib, mib, gib, tib")
		}
		u, ok := durUnits[unit]
		if !ok {
			hint := "durations use ms, s, m, h, d; sizes use b, kb, mb, gb, tb, kib, mib, gib, tib"
			if lower := strings.ToLower(unit); lower != unit {
				if _, ok1 := durUnits[lower]; ok1 {
					hint = "units are lowercase: use " + lower
				} else if _, ok2 := sizeUnits[lower]; ok2 {
					hint = "units are lowercase: use " + lower
				}
			}
			return numErr(fmt.Sprintf("unknown unit %q in %q", unit, text), hint)
		}
		if u.rank >= lastRank {
			return numErr(fmt.Sprintf("invalid duration %q: units must go from largest to smallest, each at most once", text), "write 1h30m, not 30m1h")
		}
		lastRank = u.rank
		v, err := strconv.ParseInt(d, 10, 64)
		if err != nil || v > math.MaxInt64/u.ns {
			return rangeErr()
		}
		part := v * u.ns
		if total > math.MaxInt64-part {
			return rangeErr()
		}
		total += part
		s = r[j:]
	}
	return Duration, total, "", "", ""
}
