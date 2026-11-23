// Package schema is the directive registry: which directives exist, where they
// may appear, and what their arguments must look like. The validator checks
// configuration against it, and the documentation generator reads it.
package schema

import (
	"fmt"
	"math"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/laikait/lotus/internal/config/ast"
	"github.com/laikait/lotus/internal/config/diag"
)

// Problem describes why a value was rejected. Pos, when set, points at the
// exact offending element (for example one item of a list).
type Problem struct {
	Msg  string
	Hint string
	Pos  diag.Pos
}

// Type checks one argument value.
type Type interface {
	// Name is a short description used in messages, such as "size".
	Name() string
	// Check returns nil if v is acceptable.
	Check(v ast.Value) *Problem
}

// Describe returns a short description of a value for use in messages.
// Secret values are never shown.
func Describe(v ast.Value) string {
	switch v := v.(type) {
	case *ast.String:
		if v.Secret {
			return "a string (value hidden)"
		}
		s, _ := v.Plain()
		if len(s) > 40 {
			s = s[:40] + "..."
		}
		return fmt.Sprintf("string %q", s)
	case *ast.Int:
		return "integer " + v.Source
	case *ast.Size:
		return "size " + v.Source
	case *ast.Duration:
		return "duration " + v.Source
	case *ast.Bool:
		return fmt.Sprintf("%t", v.Value)
	case *ast.Null:
		return "null"
	case *ast.Ident:
		return fmt.Sprintf("word %q", v.Name)
	case *ast.List:
		return "a list"
	case *ast.Operator:
		return "operator " + v.Op
	default:
		return "an unsupported value"
	}
}

func mismatch(want string, v ast.Value) *Problem {
	return &Problem{Msg: fmt.Sprintf("expected %s, got %s", want, Describe(v))}
}

// plainString returns the text of a resolved string value.
func plainString(v ast.Value) (string, bool) {
	s, ok := v.(*ast.String)
	if !ok {
		return "", false
	}
	return s.Plain()
}

// ---- simple types ----

type stringType struct{}

// String accepts any string.
func String() Type { return stringType{} }

func (stringType) Name() string { return "string" }
func (stringType) Check(v ast.Value) *Problem {
	if _, ok := plainString(v); !ok {
		return mismatch("a string", v)
	}
	return nil
}

type boolType struct{}

// Bool accepts true or false.
func Bool() Type { return boolType{} }

func (boolType) Name() string { return "bool" }
func (boolType) Check(v ast.Value) *Problem {
	if _, ok := v.(*ast.Bool); !ok {
		return mismatch("true or false", v)
	}
	return nil
}

type intType struct{ min, max int64 }

// Int accepts an integer within [min, max].
func Int(min, max int64) Type { return intType{min, max} }

// IntAtLeast accepts an integer >= min.
func IntAtLeast(min int64) Type { return intType{min, math.MaxInt64} }

func (t intType) Name() string { return "integer" }
func (t intType) Check(v ast.Value) *Problem {
	n, ok := v.(*ast.Int)
	if !ok {
		return mismatch("an integer", v)
	}
	if n.Value < t.min || n.Value > t.max {
		if t.max == math.MaxInt64 {
			return &Problem{Msg: fmt.Sprintf("%d is too small; the minimum is %d", n.Value, t.min)}
		}
		return &Problem{Msg: fmt.Sprintf("%d is out of range; allowed: %d to %d", n.Value, t.min, t.max)}
	}
	return nil
}

type sizeType struct{ min int64 }

// Size accepts a size (such as 10mib) of at least min bytes.
func Size(min int64) Type { return sizeType{min} }

func (t sizeType) Name() string { return "size" }
func (t sizeType) Check(v ast.Value) *Problem {
	n, ok := v.(*ast.Size)
	if !ok {
		return &Problem{
			Msg:  fmt.Sprintf("expected a size, got %s", Describe(v)),
			Hint: "sizes need a unit, e.g. 10mib (powers of 2) or 10mb (powers of 10)",
		}
	}
	if n.Bytes < t.min {
		return &Problem{Msg: fmt.Sprintf("size %s is too small; the minimum is %d bytes", n.Source, t.min)}
	}
	return nil
}

type durationType struct{ min time.Duration }

// Duration accepts a duration (such as 15s) of at least min.
func Duration(min time.Duration) Type { return durationType{min} }

func (t durationType) Name() string { return "duration" }
func (t durationType) Check(v ast.Value) *Problem {
	n, ok := v.(*ast.Duration)
	if !ok {
		return &Problem{
			Msg:  fmt.Sprintf("expected a duration, got %s", Describe(v)),
			Hint: "durations need a unit: ms, s, m, h or d, e.g. 15s or 1h30m",
		}
	}
	if n.Value < t.min {
		return &Problem{Msg: fmt.Sprintf("duration %s is too short; the minimum is %s", n.Source, t.min)}
	}
	return nil
}

// ---- enum ----

type enumType struct{ values []string }

// Enum accepts one of the given words, written bare (`https`) or quoted ("1.2").
func Enum(values ...string) Type { return enumType{values} }

func (t enumType) Name() string { return "one of " + strings.Join(t.values, "|") }
func (t enumType) Check(v ast.Value) *Problem {
	var word string
	switch v := v.(type) {
	case *ast.Ident:
		word = v.Name
	case *ast.String:
		s, ok := v.Plain()
		if !ok {
			return mismatch(t.Name(), v)
		}
		word = s
	default:
		return mismatch(t.Name(), v)
	}
	for _, ok := range t.values {
		if word == ok {
			return nil
		}
	}
	p := &Problem{Msg: fmt.Sprintf("invalid value %q; expected %s", word, t.Name())}
	if s := Suggest(word, t.values); s != "" {
		p.Hint = "did you mean " + s + "?"
	}
	return p
}

// ---- network values ----

type cidrType struct{}

// CIDR accepts an IP address or a CIDR prefix, IPv4 or IPv6.
func CIDR() Type { return cidrType{} }

func (cidrType) Name() string { return "IP address or CIDR" }
func (cidrType) Check(v ast.Value) *Problem {
	s, ok := plainString(v)
	if !ok {
		return mismatch("an IP address or CIDR string", v)
	}
	if _, err := netip.ParsePrefix(s); err == nil {
		return nil
	}
	if _, err := netip.ParseAddr(s); err == nil {
		return nil
	}
	return &Problem{Msg: fmt.Sprintf("%q is not a valid IP address or CIDR", s), Hint: `examples: "10.0.0.0/8", "2001:db8::/32", "192.0.2.1"`}
}

// AddressKinds selects which address forms an address type accepts.
type AddressKinds int

// Address forms.
const (
	HostPort AddressKinds = 1 << iota // host:port, [v6]:port, :port
	UnixSock                          // unix:/path/to/socket
)

type addressType struct{ kinds AddressKinds }

// Address accepts the given address forms.
func Address(kinds AddressKinds) Type { return addressType{kinds} }

func (t addressType) Name() string {
	var forms []string
	if t.kinds&HostPort != 0 {
		forms = append(forms, "host:port")
	}
	if t.kinds&UnixSock != 0 {
		forms = append(forms, "unix:/path")
	}
	return "address (" + strings.Join(forms, " or ") + ")"
}

var hostnameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$`)

func (t addressType) Check(v ast.Value) *Problem {
	s, ok := plainString(v)
	if !ok {
		return mismatch("an address string", v)
	}
	if rest, found := strings.CutPrefix(s, "unix:"); found {
		if t.kinds&UnixSock == 0 {
			return &Problem{Msg: "unix socket addresses are not allowed here", Hint: "use host:port"}
		}
		if rest == "" || strings.ContainsRune(rest, 0) {
			return &Problem{Msg: fmt.Sprintf("%q is not a valid unix socket address", s), Hint: `example: "unix:/run/php/php-fpm.sock"`}
		}
		return nil
	}
	if t.kinds&HostPort == 0 {
		return &Problem{Msg: fmt.Sprintf("%q is not a valid address", s), Hint: `use "unix:/path/to/socket"`}
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return &Problem{Msg: fmt.Sprintf("%q is not a valid host:port address", s), Hint: `examples: "0.0.0.0:80", "[::]:443", "127.0.0.1:9000"`}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return &Problem{Msg: fmt.Sprintf("port %q is not a number from 1 to 65535", port)}
	}
	if host != "" {
		if _, err := netip.ParseAddr(host); err != nil && !hostnameRe.MatchString(host) {
			return &Problem{Msg: fmt.Sprintf("host %q is not an IP address or a valid hostname", host)}
		}
	}
	return nil
}

// ---- paths and patterns ----

type pathType struct{}

// Path accepts a non-empty file system path.
func Path() Type { return pathType{} }

func (pathType) Name() string { return "path" }
func (pathType) Check(v ast.Value) *Problem {
	s, ok := plainString(v)
	if !ok {
		return mismatch("a path string", v)
	}
	if s == "" {
		return &Problem{Msg: "path must not be empty"}
	}
	if strings.ContainsRune(s, 0) {
		return &Problem{Msg: "path must not contain NUL bytes"}
	}
	return nil
}

type regexType struct{}

// Regex accepts a regular expression in RE2 syntax (Go's regexp package).
func Regex() Type { return regexType{} }

func (regexType) Name() string { return "regular expression" }
func (regexType) Check(v ast.Value) *Problem {
	s, ok := plainString(v)
	if !ok {
		return mismatch("a regular expression string", v)
	}
	if _, err := regexp.Compile(s); err != nil {
		return &Problem{
			Msg:  "invalid regular expression: " + strings.TrimPrefix(err.Error(), "error parsing regexp: "),
			Hint: "LOTUS uses RE2 syntax: no backreferences or lookaround",
		}
	}
	return nil
}

// ---- combinators ----

type listType struct {
	elem   Type
	minLen int
}

// List accepts a list of at least minLen items, each matching elem.
func List(elem Type, minLen int) Type { return listType{elem, minLen} }

func (t listType) Name() string { return "list of " + t.elem.Name() }
func (t listType) Check(v ast.Value) *Problem {
	l, ok := v.(*ast.List)
	if !ok {
		return mismatch("a list like [ ... ]", v)
	}
	if len(l.Items) < t.minLen {
		return &Problem{Msg: fmt.Sprintf("list needs at least %d item(s)", t.minLen)}
	}
	for _, item := range l.Items {
		if p := t.elem.Check(item); p != nil {
			if !p.Pos.IsValid() {
				p.Pos = item.Position()
			}
			return p
		}
	}
	return nil
}

type oneOfType struct{ types []Type }

// OneOf accepts a value that matches any of the given types.
func OneOf(types ...Type) Type { return oneOfType{types} }

func (t oneOfType) Name() string {
	names := make([]string, len(t.types))
	for i, ty := range t.types {
		names[i] = ty.Name()
	}
	return strings.Join(names, " or ")
}

func (t oneOfType) Check(v ast.Value) *Problem {
	for _, ty := range t.types {
		if ty.Check(v) == nil {
			return nil
		}
	}
	return mismatch(t.Name(), v)
}

type nullableType struct{ inner Type }

// Nullable accepts null (turning off an inherited setting) or the inner type.
func Nullable(inner Type) Type { return nullableType{inner} }

func (t nullableType) Name() string { return t.inner.Name() + " or null" }
func (t nullableType) Check(v ast.Value) *Problem {
	if _, ok := v.(*ast.Null); ok {
		return nil
	}
	return t.inner.Check(v)
}

type operatorType struct{ ops []string }

// Operator accepts one of the given operators (= ~ ~* ^~ !~ !~*).
func Operator(ops ...string) Type { return operatorType{ops} }

func (t operatorType) Name() string { return "operator (" + strings.Join(t.ops, " ") + ")" }
func (t operatorType) Check(v ast.Value) *Problem {
	op, ok := v.(*ast.Operator)
	if !ok {
		return mismatch(t.Name(), v)
	}
	for _, o := range t.ops {
		if op.Op == o {
			return nil
		}
	}
	return &Problem{Msg: fmt.Sprintf("operator %s is not allowed here; allowed: %s", op.Op, strings.Join(t.ops, " "))}
}

// ---- suggestions ----

// Suggest returns the candidate closest to word (edit distance up to 2, and
// less than the word's length), formatted in quotes, or "" if none is close.
func Suggest(word string, candidates []string) string {
	best, bestDist := "", 3
	for _, c := range candidates {
		if d := editDistance(word, c); d < bestDist && d < len(word) {
			best, bestDist = c, d
		}
	}
	if best == "" {
		return ""
	}
	return strconv.Quote(best)
}

func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

// ---- hostnames ----

var hostLabelRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

type hostnameType struct{}

// Hostname accepts an ASCII hostname such as example.com, or a wildcard such
// as *.example.com. Internationalized names must be written in punycode.
func Hostname() Type { return hostnameType{} }

func (hostnameType) Name() string { return "hostname" }
func (hostnameType) Check(v ast.Value) *Problem {
	s, ok := plainString(v)
	if !ok {
		return mismatch("a hostname string", v)
	}
	if !ValidHostname(s) {
		return &Problem{
			Msg:  fmt.Sprintf("%q is not a valid hostname", s),
			Hint: `use ASCII names like "example.com" or "*.example.com" (write internationalized names in punycode, e.g. "xn--...")`,
		}
	}
	return nil
}

// ValidHostname reports whether s is a valid hostname or wildcard hostname.
func ValidHostname(s string) bool {
	rest := strings.TrimPrefix(s, "*.")
	if rest == "" || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(rest, ".") {
		if !hostLabelRe.MatchString(label) {
			return false
		}
	}
	return true
}
