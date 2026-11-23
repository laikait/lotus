// Package diag defines source positions and diagnostics shared by every stage
// of configuration processing (lexer, parser, validator, compiler).
package diag

import "fmt"

// Severity of a diagnostic.
type Severity int

// Severities.
const (
	Error Severity = iota
	Warning
	Note
)

func (s Severity) String() string {
	switch s {
	case Error:
		return "error"
	case Warning:
		return "warning"
	default:
		return "note"
	}
}

// MarshalText makes severities appear as strings in JSON output.
func (s Severity) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// Pos is a position in a source file. Line and Col start at 1; Col counts
// Unicode code points. Offset is a byte offset into the file.
type Pos struct {
	File   string `json:"file"`
	Offset int    `json:"offset"`
	Line   int    `json:"line"`
	Col    int    `json:"column"`
}

// IsValid reports whether the position points somewhere.
func (p Pos) IsValid() bool { return p.Line > 0 }

func (p Pos) String() string {
	file := p.File
	if file == "" {
		file = "<input>"
	}
	return fmt.Sprintf("%s:%d:%d", file, p.Line, p.Col)
}

// Diagnostic is one error, warning or note.
type Diagnostic struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`
	Pos      Pos      `json:"pos"`
	Message  string   `json:"message"`
	Hint     string   `json:"hint,omitempty"`
}

func (d Diagnostic) Error() string {
	s := fmt.Sprintf("%s: %s [%s]: %s", d.Pos, d.Severity, d.Code, d.Message)
	if d.Hint != "" {
		s += " (hint: " + d.Hint + ")"
	}
	return s
}

// DefaultMaxErrors is the number of errors reported before processing stops.
const DefaultMaxErrors = 50

// List collects diagnostics and caps the number of errors.
type List struct {
	items []Diagnostic
	errs  int
	max   int
}

// NewList returns a list that stops accepting errors after max of them.
func NewList(max int) *List { return &List{max: max} }

// Full reports whether the error limit has been reached.
func (l *List) Full() bool { return l.errs >= l.max }

// Add records a diagnostic. Errors past the limit are dropped; when the limit
// is reached a final note is added.
func (l *List) Add(d Diagnostic) {
	if d.Severity == Error {
		if l.Full() {
			return
		}
		l.errs++
		l.items = append(l.items, d)
		if l.Full() {
			l.items = append(l.items, Diagnostic{
				Severity: Note,
				Code:     CodeTooManyErrors,
				Pos:      d.Pos,
				Message:  fmt.Sprintf("too many errors (%d); stopping", l.max),
			})
		}
		return
	}
	l.items = append(l.items, d)
}

// Error records an error without a hint.
func (l *List) Error(pos Pos, code, msg string) {
	l.Add(Diagnostic{Severity: Error, Code: code, Pos: pos, Message: msg})
}

// ErrorHint records an error with a hint.
func (l *List) ErrorHint(pos Pos, code, msg, hint string) {
	l.Add(Diagnostic{Severity: Error, Code: code, Pos: pos, Message: msg, Hint: hint})
}

// HasErrors reports whether any error was recorded.
func (l *List) HasErrors() bool { return l.errs > 0 }

// Items returns the recorded diagnostics in the order they were reported.
func (l *List) Items() []Diagnostic { return l.items }
