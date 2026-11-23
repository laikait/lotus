package clientip

import (
	"net/http"
	"net/netip"
	"testing"
)

func prefixes(s ...string) []netip.Prefix {
	var out []netip.Prefix
	for _, x := range s {
		out = append(out, netip.MustParsePrefix(x))
	}
	return out
}

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return h
}

func TestResolve(t *testing.T) {
	r := New(prefixes("10.0.0.0/8", "192.0.2.7/32", "fd00::/8"))
	tests := []struct {
		name string
		peer string
		h    http.Header
		want string
	}{
		{"untrusted peer: headers are ignored", "203.0.113.9", hdr("X-Forwarded-For", "1.2.3.4"), "203.0.113.9"},
		{"untrusted peer: Forwarded is ignored too", "203.0.113.9", hdr("Forwarded", "for=1.2.3.4"), "203.0.113.9"},
		{"trusted peer, no headers", "10.0.0.5", nil, "10.0.0.5"},
		{"trusted peer, one client", "10.0.0.5", hdr("X-Forwarded-For", "198.51.100.4"), "198.51.100.4"},
		{"two trusted hops are skipped", "10.0.0.5", hdr("X-Forwarded-For", "198.51.100.4, 10.0.0.9"), "198.51.100.4"},
		{"a spoofed left entry is not believed",
			"10.0.0.5", hdr("X-Forwarded-For", "6.6.6.6, 198.51.100.4"), "198.51.100.4"},
		{"spoofed entry behind a trusted hop",
			"10.0.0.5", hdr("X-Forwarded-For", "6.6.6.6, 198.51.100.4, 10.0.0.9"), "198.51.100.4"},
		{"every address trusted: the leftmost", "10.0.0.5", hdr("X-Forwarded-For", "10.1.1.1, 10.2.2.2"), "10.1.1.1"},
		{"several header lines are one chain", "10.0.0.5",
			hdr("X-Forwarded-For", "6.6.6.6", "X-Forwarded-For", "198.51.100.4"), "198.51.100.4"},
		{"unknown stops the walk at the nearest known hop", "10.0.0.5", hdr("X-Forwarded-For", "198.51.100.4, unknown"), "10.0.0.5"},
		{"garbage on the right", "10.0.0.5", hdr("X-Forwarded-For", "198.51.100.4, <script>"), "10.0.0.5"},
		{"empty header", "10.0.0.5", hdr("X-Forwarded-For", " , "), "10.0.0.5"},
		{"port is dropped", "10.0.0.5", hdr("X-Forwarded-For", "198.51.100.4:5555"), "198.51.100.4"},
		{"ipv6 client", "10.0.0.5", hdr("X-Forwarded-For", "2001:db8::1"), "2001:db8::1"},
		{"bracketed ipv6 with port", "10.0.0.5", hdr("X-Forwarded-For", "[2001:db8::1]:443"), "2001:db8::1"},
		{"ipv6 trusted peer", "fd00::1", hdr("X-Forwarded-For", "2001:db8::2"), "2001:db8::2"},
		{"ipv4-mapped client is unmapped", "10.0.0.5", hdr("X-Forwarded-For", "::ffff:198.51.100.4"), "198.51.100.4"},
		{"ipv4-mapped trusted peer", "::ffff:10.0.0.5", hdr("X-Forwarded-For", "198.51.100.4"), "198.51.100.4"},
		{"single-address prefix", "192.0.2.7", hdr("X-Forwarded-For", "198.51.100.4"), "198.51.100.4"},
		{"neighbor of a /32 is untrusted", "192.0.2.8", hdr("X-Forwarded-For", "198.51.100.4"), "192.0.2.8"},

		// Forwarded (RFC 7239)
		{"Forwarded for=", "10.0.0.5", hdr("Forwarded", "for=198.51.100.4;proto=https"), "198.51.100.4"},
		{"Forwarded quoted ipv6 with port", "10.0.0.5", hdr("Forwarded", `for="[2001:db8::1]:4711"`), "2001:db8::1"},
		{"Forwarded chain", "10.0.0.5", hdr("Forwarded", "for=6.6.6.6, for=198.51.100.4, for=10.0.0.9"), "198.51.100.4"},
		{"Forwarded is case-insensitive", "10.0.0.5", hdr("forwarded", "For=198.51.100.4"), "198.51.100.4"},
		{"Forwarded obfuscated node", "10.0.0.5", hdr("Forwarded", "for=_hidden"), "10.0.0.5"},
		{"Forwarded wins over X-Forwarded-For", "10.0.0.5",
			hdr("Forwarded", "for=198.51.100.4", "X-Forwarded-For", "6.6.6.6"), "198.51.100.4"},
		{"Forwarded without for= falls back to XFF", "10.0.0.5",
			hdr("Forwarded", "proto=https;by=10.0.0.9", "X-Forwarded-For", "198.51.100.4"), "198.51.100.4"},
		{"a comma inside quotes does not split", "10.0.0.5",
			hdr("Forwarded", `for="198.51.100.4";by="a,b"`), "198.51.100.4"},
	}
	for _, tt := range tests {
		got := r.Resolve(netip.MustParseAddr(tt.peer), tt.h)
		if got != netip.MustParseAddr(tt.want) {
			t.Errorf("%s: got %s, want %s", tt.name, got, tt.want)
		}
	}
}

func TestNoTrustedProxiesMeansPeerOnly(t *testing.T) {
	r := New(nil)
	got := r.Resolve(netip.MustParseAddr("10.0.0.5"), hdr("X-Forwarded-For", "1.2.3.4"))
	if got != netip.MustParseAddr("10.0.0.5") {
		t.Errorf("got %s", got)
	}
}

func TestInvalidPeerIsReturnedAsIs(t *testing.T) {
	r := New(prefixes("0.0.0.0/0"))
	if got := r.Resolve(netip.Addr{}, hdr("X-Forwarded-For", "1.2.3.4")); got.IsValid() {
		t.Errorf("an unknown peer (unix socket) must not be replaced by header data: %s", got)
	}
}

func TestLongChainsAreBounded(t *testing.T) {
	r := New(prefixes("10.0.0.0/8"))
	v := ""
	for i := 0; i < 1000; i++ {
		v += "10.0.0.1, "
	}
	v = "198.51.100.4, " + v + "10.0.0.2"
	// The client is beyond the hop limit, so it is not believed: the answer
	// is the leftmost address inside the window we examine.
	got := r.Resolve(netip.MustParseAddr("10.0.0.5"), hdr("X-Forwarded-For", v))
	if got != netip.MustParseAddr("10.0.0.1") {
		t.Errorf("got %s", got)
	}
}

func TestMappedPrefixIsNormalized(t *testing.T) {
	r := New(prefixes("::ffff:10.0.0.0/104"))
	got := r.Resolve(netip.MustParseAddr("10.9.9.9"), hdr("X-Forwarded-For", "198.51.100.4"))
	if got != netip.MustParseAddr("198.51.100.4") {
		t.Errorf("got %s", got)
	}
}

func TestParseNode(t *testing.T) {
	good := map[string]string{
		"1.2.3.4":            "1.2.3.4",
		"1.2.3.4:80":         "1.2.3.4",
		"2001:db8::1":        "2001:db8::1",
		"[2001:db8::1]":      "2001:db8::1",
		"[2001:db8::1]:8080": "2001:db8::1",
		" 1.2.3.4 ":          "1.2.3.4",
		"fe80::1%eth0":       "fe80::1", // zones are dropped
	}
	for in, want := range good {
		got, ok := parseNode(in)
		if !ok || got.String() != want {
			t.Errorf("parseNode(%q) = %s, %v; want %s", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "unknown", "_abc", "1.2.3", "1.2.3.4:", "1.2.3.4:99999x", "[2001:db8::1", "[2001:db8::1]x",
		"1.2.3.4:80:90x", "example.com", "1.2.3.4/32", "999.1.1.1"} {
		if got, ok := parseNode(bad); ok {
			t.Errorf("parseNode(%q) accepted: %s", bad, got)
		}
	}
}

func FuzzResolve(f *testing.F) {
	f.Add("198.51.100.4, 10.0.0.9", `for="[2001:db8::1]:4711";proto=https`)
	f.Add("", `for=a,for="`)
	f.Add("\x00,,,", `for=\"`)
	r := New(prefixes("10.0.0.0/8"))
	peer := netip.MustParseAddr("10.0.0.5")
	f.Fuzz(func(t *testing.T, xff, fwd string) {
		h := http.Header{}
		h.Add("X-Forwarded-For", xff)
		h.Add("Forwarded", fwd)
		got := r.Resolve(peer, h)
		if !got.IsValid() {
			t.Fatalf("Resolve returned an invalid address for xff=%q fwd=%q", xff, fwd)
		}
	})
}
