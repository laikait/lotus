// Package vhost picks the site that should answer a request, from the Host
// header or the TLS server name. Lookup order (spec §4.2):
//
//  1. exact hostname;
//  2. the longest matching wildcard (*.example.com matches a.example.com and
//     a.b.example.com, but not example.com);
//  3. the listener's default site.
//
// A Table is immutable after Build and safe for concurrent use.
package vhost

import (
	"net"
	"sort"
	"strings"
)

// Table maps hostnames to values (typically a site).
type Table[T any] struct {
	exact     map[string]T
	wildcards []wildcard[T] // longest suffix first
	def       *T
}

type wildcard[T any] struct {
	suffix string // ".example.com"
	value  T
}

// Entry names one site and the hostnames that select it.
type Entry[T any] struct {
	Value T
	Hosts []string // exact names and "*.suffix" wildcards, already lowercase
}

// Build creates a table. The first entry is the default for requests whose
// host matches nothing. If two entries claim the same name, the first wins
// (the compiler rejects such configurations, so this only matters in tests).
func Build[T any](entries []Entry[T]) *Table[T] {
	t := &Table[T]{exact: map[string]T{}}
	seenWild := map[string]bool{}
	for i, e := range entries {
		if i == 0 {
			v := e.Value
			t.def = &v
		}
		for _, h := range e.Hosts {
			if rest, ok := strings.CutPrefix(h, "*."); ok {
				suffix := "." + rest
				if !seenWild[suffix] {
					seenWild[suffix] = true
					t.wildcards = append(t.wildcards, wildcard[T]{suffix, e.Value})
				}
				continue
			}
			if _, dup := t.exact[h]; !dup {
				t.exact[h] = e.Value
			}
		}
	}
	sort.SliceStable(t.wildcards, func(i, j int) bool { return len(t.wildcards[i].suffix) > len(t.wildcards[j].suffix) })
	return t
}

// Lookup finds the value for host, which must be normalized (see NormalizeHost).
// matched is false if the default was used. ok is false only if the table is empty.
func (t *Table[T]) Lookup(host string) (value T, matched, ok bool) {
	if v, found := t.exact[host]; found {
		return v, true, true
	}
	for _, w := range t.wildcards {
		if len(host) > len(w.suffix) && strings.HasSuffix(host, w.suffix) {
			return w.value, true, true
		}
	}
	if t.def != nil {
		return *t.def, false, true
	}
	var zero T
	return zero, false, false
}

// NormalizeHost turns a Host header value or SNI name into a lookup key: the
// port is removed, the name is lowercased and a trailing dot is dropped. ok is
// false for values that can't be a hostname (empty, spaces, slashes, ...), so
// callers can answer 400 instead of guessing.
func NormalizeHost(hostport string) (host string, ok bool) {
	host = hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1] // bracketed IPv6 without a port
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" || len(host) > 253 {
		return "", false
	}
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.', r == '_', r == ':':
		default:
			return "", false
		}
	}
	return host, true
}
