package vhost

import "testing"

func table() *Table[string] {
	return Build([]Entry[string]{
		{Value: "main", Hosts: []string{"example.com", "www.example.com"}},
		{Value: "wild", Hosts: []string{"*.example.com"}},
		{Value: "deep", Hosts: []string{"*.eu.example.com", "eu.example.com"}},
		{Value: "other", Hosts: []string{"other.test", "*.other.test"}},
	})
}

func TestLookup(t *testing.T) {
	tests := []struct {
		host    string
		want    string
		matched bool
	}{
		{"example.com", "main", true},
		{"www.example.com", "main", true},  // exact beats the wildcard
		{"shop.example.com", "wild", true}, // wildcard
		{"a.b.example.com", "wild", true},  // wildcards match several labels
		{"x.eu.example.com", "deep", true}, // the longest wildcard wins
		{"eu.example.com", "deep", true},   // exact entry for the bare suffix
		{"other.test", "other", true},      // *.other.test does not match the bare name, but the exact entry does
		{"deep.sub.other.test", "other", true},
		{"unknown.test", "main", false},          // default site
		{"example.com.evil.test", "main", false}, // must not match by prefix
		{"notexample.com", "main", false},        // a wildcard needs the dot
		{"", "main", false},
	}
	tbl := table()
	for _, tt := range tests {
		got, matched, ok := tbl.Lookup(tt.host)
		if !ok || got != tt.want || matched != tt.matched {
			t.Errorf("Lookup(%q) = %q matched=%v, want %q matched=%v", tt.host, got, matched, tt.want, tt.matched)
		}
	}
}

func TestWildcardDoesNotMatchItsOwnApex(t *testing.T) {
	tbl := Build([]Entry[string]{{Value: "a", Hosts: []string{"first.test"}}, {Value: "w", Hosts: []string{"*.example.com"}}})
	if got, matched, _ := tbl.Lookup("example.com"); matched || got != "a" {
		t.Errorf("*.example.com must not match example.com: got %q matched=%v", got, matched)
	}
}

func TestEmptyTable(t *testing.T) {
	if _, _, ok := Build[string](nil).Lookup("a.test"); ok {
		t.Error("an empty table has no default")
	}
}

func TestFirstClaimWins(t *testing.T) {
	tbl := Build([]Entry[string]{{Value: "one", Hosts: []string{"a.test", "*.b.test"}}, {Value: "two", Hosts: []string{"a.test", "*.b.test"}}})
	if got, _, _ := tbl.Lookup("a.test"); got != "one" {
		t.Errorf("got %q", got)
	}
	if got, _, _ := tbl.Lookup("x.b.test"); got != "one" {
		t.Errorf("got %q", got)
	}
}

func TestNormalizeHost(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"Example.COM", "example.com", true},
		{"example.com:8080", "example.com", true},
		{"example.com.", "example.com", true},
		{"EXAMPLE.com.:443", "example.com", true},
		{"127.0.0.1:80", "127.0.0.1", true},
		{"[::1]:8080", "::1", true},
		{"[::1]", "::1", true},
		{"localhost", "localhost", true},
		{"my_host.test", "my_host.test", true},
		{"", "", false},
		{".", "", false},
		{":80", "", false},
		{"a b.test", "", false},
		{"a/b.test", "", false},
		{"a.test/../x", "", false},
		{"a\x00b", "", false},
		{"a\r\nb", "", false},
		{"ex@mple.com", "", false},
		{"münchen.de", "", false}, // non-ASCII: must arrive as punycode
	}
	for _, tt := range tests {
		got, ok := NormalizeHost(tt.in)
		if ok != tt.ok || got != tt.want {
			t.Errorf("NormalizeHost(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}
