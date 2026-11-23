package htaccess

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Access control: Apache 2.4 `Require` and the older `Order`/`Allow`/`Deny`.
//
// Supported: `Require all granted|denied`, `Require ip ...`, `Require not ip
// ...`, and <RequireAll>, <RequireAny>, <RequireNone> around them. Several
// Require lines at the top level mean "any of them". Everything that needs a
// user (valid-user, user, group), a host name (DNS), an expression or an
// environment variable is not supported, and is refused rather than guessed.

type reqKind int

const (
	rqAny reqKind = iota
	rqAll
	rqNone
	rqGranted
	rqDenied
	rqIP
)

type reqNode struct {
	kind reqKind
	nets []netip.Prefix
	not  bool // for rqIP: `Require not ip`
	kids []*reqNode
}

func (n *reqNode) eval(ip netip.Addr) bool {
	switch n.kind {
	case rqGranted:
		return true
	case rqDenied:
		return false
	case rqIP:
		return inAny(n.nets, ip) != n.not
	case rqAny:
		for _, k := range n.kids {
			if k.eval(ip) {
				return true
			}
		}
		return false
	case rqAll:
		for _, k := range n.kids {
			if !k.eval(ip) {
				return false
			}
		}
		return true
	case rqNone:
		for _, k := range n.kids {
			if k.eval(ip) {
				return false
			}
		}
		return true
	}
	return false
}

func inAny(nets []netip.Prefix, ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	ip = ip.Unmap()
	for _, p := range nets {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// legacyAccess is Order/Allow/Deny.
type legacyAccess struct {
	allowFirst           bool // Order Allow,Deny (default Deny,Allow)
	allowAll, denyAll    bool
	allow, deny          []netip.Prefix
	orderSet, rulesFound bool
}

func (l *legacyAccess) permits(ip netip.Addr) bool {
	allowed := l.allowAll || inAny(l.allow, ip)
	denied := l.denyAll || inAny(l.deny, ip)
	if l.allowFirst {
		return allowed && !denied // default: denied
	}
	return !(denied && !allowed) // default: allowed
}

// accessSet is the access rules of one section (a directory's .htaccess, or one
// <Files> block in it). Exactly one of the two forms is used.
type accessSet struct {
	line   int
	top    *reqNode // implicit RequireAny over the top-level Require lines
	legacy *legacyAccess
}

func (a *accessSet) permits(ip netip.Addr) bool {
	if a.top != nil {
		return a.top.eval(ip)
	}
	return a.legacy.permits(ip)
}

// parseNets reads the addresses of `Require ip`, `Allow from` and `Deny from`:
// an address, a CIDR range, an address with a netmask, or a partial IPv4
// address such as "10" or "192.168" (the older notation).
func parseNets(words []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, w := range words {
		p, err := parseNet(w)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func parseNet(w string) (netip.Prefix, error) {
	if addr, mask, ok := strings.Cut(w, "/"); ok {
		a, err := netip.ParseAddr(addr)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("%q is not an IP address range", w)
		}
		if bits, err := strconv.Atoi(mask); err == nil {
			p, err := a.Prefix(bits)
			if err != nil {
				return netip.Prefix{}, fmt.Errorf("%q is not an IP address range", w)
			}
			return p, nil
		}
		if m, err := netip.ParseAddr(mask); err == nil && a.Is4() && m.Is4() { // 10.0.0.0/255.0.0.0
			bits := 0
			for _, b := range m.As4() {
				for ; b&0x80 != 0; b <<= 1 {
					bits++
				}
				if b != 0 {
					return netip.Prefix{}, fmt.Errorf("%q has a netmask that is not contiguous", w)
				}
			}
			return a.Prefix(bits)
		}
		return netip.Prefix{}, fmt.Errorf("%q is not an IP address range", w)
	}
	if a, err := netip.ParseAddr(w); err == nil {
		return netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()), nil
	}
	// Partial IPv4: "10", "192.168", "172.16.5".
	parts := strings.Split(w, ".")
	if len(parts) >= 1 && len(parts) <= 3 {
		var oct [4]byte
		ok := true
		for i, s := range parts {
			n, err := strconv.Atoi(s)
			if err != nil || n < 0 || n > 255 {
				ok = false
				break
			}
			oct[i] = byte(n)
		}
		if ok {
			return netip.PrefixFrom(netip.AddrFrom4(oct), 8*len(parts)), nil
		}
	}
	return netip.Prefix{}, fmt.Errorf("%q is not an IP address or range (host names need DNS lookups, which LOTUS does not do)", w)
}

// requireLeaf compiles one `Require ...` line. inContainer says it sits in a
// <RequireAll> or <RequireNone>, the only places `not` makes sense.
func requireLeaf(args []string, inAllOrNone bool) (*reqNode, string) {
	if len(args) == 0 {
		return nil, "Require needs an argument"
	}
	not := false
	if strings.EqualFold(args[0], "not") {
		not, args = true, args[1:]
		if len(args) == 0 {
			return nil, "Require not needs an argument"
		}
		if !inAllOrNone {
			return nil, "`Require not` only works inside <RequireAll> or <RequireNone> (on its own it never grants access)"
		}
	}
	switch strings.ToLower(args[0]) {
	case "all":
		if len(args) != 2 {
			return nil, "use `Require all granted` or `Require all denied`"
		}
		switch strings.ToLower(args[1]) {
		case "granted":
			return &reqNode{kind: map[bool]reqKind{false: rqGranted, true: rqDenied}[not]}, ""
		case "denied":
			return &reqNode{kind: map[bool]reqKind{false: rqDenied, true: rqGranted}[not]}, ""
		}
		return nil, "use `Require all granted` or `Require all denied`"
	case "ip":
		nets, err := parseNets(args[1:])
		if err != nil {
			return nil, err.Error()
		}
		if len(nets) == 0 {
			return nil, "Require ip needs at least one address"
		}
		return &reqNode{kind: rqIP, nets: nets, not: not}, ""
	case "valid-user", "user", "group", "ldap-user", "ldap-group", "ldap-dn", "ldap-attribute", "ldap-filter":
		return nil, "Require " + args[0] + " needs authentication, which is not supported yet"
	case "host":
		return nil, "Require host needs DNS lookups, which LOTUS does not do; use `Require ip`"
	}
	return nil, "Require " + args[0] + " is not supported (only `all granted`, `all denied`, `ip` and `not ip`)"
}

// compileRequireContainer compiles <RequireAll>, <RequireAny> or <RequireNone>.
func compileRequireContainer(n *Node, report func(line int, msg string)) *reqNode {
	kind := map[string]reqKind{"requireall": rqAll, "requireany": rqAny, "requirenone": rqNone}[n.Name]
	out := &reqNode{kind: kind}
	allOrNone := kind == rqAll || kind == rqNone
	for _, ch := range n.Children {
		switch {
		case ch.Container && (ch.Name == "requireall" || ch.Name == "requireany" || ch.Name == "requirenone"):
			if k := compileRequireContainer(ch, report); k != nil {
				out.kids = append(out.kids, k)
			}
		case !ch.Container && ch.Name == "require":
			leaf, msg := requireLeaf(ch.Args, allOrNone)
			if msg != "" {
				report(ch.Line, msg)
				continue
			}
			out.kids = append(out.kids, leaf)
		default:
			report(ch.Line, fmt.Sprintf("%s is not allowed inside <%s>", ch.Name, n.Name))
		}
	}
	if len(out.kids) == 0 {
		report(n.Line, fmt.Sprintf("<%s> has nothing inside it", n.Name))
		return nil
	}
	return out
}
