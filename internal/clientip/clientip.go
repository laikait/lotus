// Package clientip works out the address of the real client when LOTUS sits
// behind load balancers or other proxies (spec §4.10.4).
//
// The rule: forwarding headers are believed only when the directly connected
// peer is in trusted_proxies. Then the chain is read from the right, skipping
// every address that is itself a trusted proxy, and the first address that is
// not trusted is the client. Reading from the right matters: a client can put
// anything it likes at the left of X-Forwarded-For, but each trusted proxy
// appends the address it actually saw at the right.
package clientip

import (
	"net/http"
	"net/netip"
	"strings"
)

// maxHops bounds how many forwarded addresses are examined. A chain longer
// than this is never legitimate, and the limit keeps the work per request fixed.
const maxHops = 64

// Resolver finds the client address of a request.
type Resolver struct {
	trusted []netip.Prefix
}

// New returns a Resolver that trusts the given proxy networks. With none, the
// client is always the directly connected peer.
func New(trusted []netip.Prefix) *Resolver {
	cp := make([]netip.Prefix, len(trusted))
	for i, p := range trusted {
		cp[i] = netip.PrefixFrom(p.Addr().Unmap(), unmapBits(p)).Masked()
	}
	return &Resolver{trusted: cp}
}

// unmapBits keeps the prefix length meaningful when a "::ffff:a.b.c.d/N"
// prefix is turned into IPv4 form.
func unmapBits(p netip.Prefix) int {
	if p.Addr().Is4In6() && p.Bits() >= 96 {
		return p.Bits() - 96
	}
	return p.Bits()
}

func (r *Resolver) isTrusted(a netip.Addr) bool {
	a = a.Unmap().WithZone("")
	for _, p := range r.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Resolve returns the client address for a request that arrived from peer with
// the given headers. If peer is not trusted, or the headers hold nothing
// usable, the answer is peer itself.
func (r *Resolver) Resolve(peer netip.Addr, h http.Header) netip.Addr {
	if !peer.IsValid() {
		return peer
	}
	peer = peer.Unmap().WithZone("")
	if len(r.trusted) == 0 || !r.isTrusted(peer) {
		return peer
	}

	chain, ok := forwardedChain(h)
	if !ok {
		chain = xffChain(h)
	}
	if len(chain) > maxHops {
		chain = chain[len(chain)-maxHops:] // the right end is the part the trusted proxies wrote
	}

	current := peer
	for i := len(chain) - 1; i >= 0; i-- {
		addr, ok := parseNode(chain[i])
		if !ok {
			// "unknown", an obfuscated identifier or garbage: we can't see past
			// it, so the nearest address we do know is the answer.
			return current
		}
		if !r.isTrusted(addr) {
			return addr
		}
		current = addr
	}
	return current
}

// xffChain returns the X-Forwarded-For entries, left to right, across all
// header lines.
func xffChain(h http.Header) []string {
	var out []string
	for _, line := range h.Values("X-Forwarded-For") {
		for _, part := range strings.Split(line, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

// forwardedChain returns the for= values of the Forwarded header (RFC 7239),
// left to right. ok is false when the header is absent or has no for= value,
// so the caller can fall back to X-Forwarded-For.
func forwardedChain(h http.Header) (chain []string, ok bool) {
	for _, line := range h.Values("Forwarded") {
		for _, element := range splitQuoted(line, ',') {
			for _, pair := range splitQuoted(element, ';') {
				name, value, found := strings.Cut(strings.TrimSpace(pair), "=")
				if !found || !strings.EqualFold(strings.TrimSpace(name), "for") {
					continue
				}
				chain = append(chain, unquote(strings.TrimSpace(value)))
			}
		}
	}
	return chain, len(chain) > 0
}

// splitQuoted splits s at sep, ignoring separators inside double quotes.
func splitQuoted(s string, sep byte) []string {
	var out []string
	inQuote, escaped, start := false, false, 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case escaped:
			escaped = false
		case c == '\\' && inQuote:
			escaped = true
		case c == '"':
			inQuote = !inQuote
		case c == sep && !inQuote:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
		s = strings.ReplaceAll(s, `\"`, `"`)
		s = strings.ReplaceAll(s, `\\`, `\`)
	}
	return s
}

// parseNode reads an address from a forwarding header value. It accepts a
// bare IPv4 or IPv6 address, "ip:port", and "[ipv6]" with or without a port.
// Names, "unknown" and "_obfuscated" tokens are rejected.
func parseNode(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return netip.Addr{}, false
	case s[0] == '[':
		end := strings.IndexByte(s, ']')
		if end < 0 {
			return netip.Addr{}, false
		}
		if rest := s[end+1:]; rest != "" && !validPortSuffix(rest) {
			return netip.Addr{}, false
		}
		s = s[1:end]
	case strings.Count(s, ":") == 1: // IPv4 with a port
		host, port, _ := strings.Cut(s, ":")
		if !validPortSuffix(":" + port) {
			return netip.Addr{}, false
		}
		s = host
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap().WithZone(""), true
}

// validPortSuffix accepts ":" followed by 1 to 5 digits.
func validPortSuffix(s string) bool {
	if len(s) < 2 || len(s) > 6 || s[0] != ':' {
		return false
	}
	for _, c := range s[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
