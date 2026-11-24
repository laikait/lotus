package proxy

import "testing"

func TestParseBackend(t *testing.T) {
	ok := map[string]BackendSpec{
		"http://10.0.0.1:8080":   {Network: "tcp", Address: "10.0.0.1:8080", Host: "10.0.0.1"},
		"http://example.com":     {Network: "tcp", Address: "example.com:80", Host: "example.com"},
		"https://example.com":    {Network: "tcp", Address: "example.com:443", TLS: true, Host: "example.com"},
		"https://api.internal:9": {Network: "tcp", Address: "api.internal:9", TLS: true, Host: "api.internal"},
		"http://[::1]:8080/":     {Network: "tcp", Address: "[::1]:8080", Host: "::1"},
		"unix:/run/app.sock":     {Network: "unix", Address: "/run/app.sock"},
	}
	for in, want := range ok {
		got, err := ParseBackend(in)
		if err != nil || got != want {
			t.Errorf("%q = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "example.com", "example.com:80", "ftp://h", "http://", "http:///x", "https://u:p@h", "http://h/api", "http://h?x=1",
		"http://h#f", "unix:", "tcp://h:1", "http://h:99999999999", "//h", "http://ho st",
	} {
		if _, err := ParseBackend(in); err == nil {
			t.Errorf("%q was accepted", in)
		}
	}
}
