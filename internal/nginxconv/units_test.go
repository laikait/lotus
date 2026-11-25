package nginxconv

import (
	"strings"
	"testing"
)

func TestDuration(t *testing.T) {
	for in, want := range map[string]string{
		"10": "10s", "30s": "30s", "5m": "5m", "1h": "1h", "1d": "1d", "250ms": "250ms", "1h30m": "1h30m", "90m": "1h30m",
		"1w": "7d", "1M": "30d", "65": "1m5s", "0": "0s", "1d12h": "1d12h", "1500ms": "1s500ms",
	} {
		got, ok := duration(in)
		if !ok || got != want {
			t.Errorf("duration(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "abc", "10x", "-5s", "1.5s", "s", "99999999999999999999"} {
		if got, ok := duration(in); ok {
			t.Errorf("duration(%q) = %q, want an error", in, got)
		}
	}
}

func TestSize(t *testing.T) {
	for in, want := range map[string]string{"1k": "1kib", "128K": "128kib", "10m": "10mib", "1g": "1gib", "512": "512b", "0": "0b"} {
		got, _, ok := size(in)
		if !ok || got != want {
			t.Errorf("size(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if _, n, _ := size("2m"); n != 2<<20 {
		t.Errorf("2m is %d bytes", n)
	}
	for _, in := range []string{"", "m", "1.5m", "10x", "-1", "1t"} {
		if got, _, ok := size(in); ok {
			t.Errorf("size(%q) = %q, want an error", in, got)
		}
	}
}

func TestQuote(t *testing.T) {
	for in, want := range map[string]string{
		`plain`:       `"plain"`,
		`say "hi"`:    `"say \"hi\""`,
		`back\slash`:  `"back\\slash"`,
		`{uri}`:       `"\{uri\}"`,
		`a${b}`:       `"a\$\{b\}"`,
		"line\nbreak": `"line\nbreak"`,
		"tab\there":   `"tab\there"`,
		"\x01":        `"\u{1}"`,
		`cost: $5`:    `"cost: $5"`,
	} {
		if got := quote(in); got != want {
			t.Errorf("quote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestRaw(t *testing.T) {
	if r, ok := raw(`^/a\.b$`); !ok || r != `'^/a\.b$'` {
		t.Errorf("raw = %q, %v", r, ok)
	}
	for _, bad := range []string{"it's", "two\nlines", "\x01"} {
		if _, ok := raw(bad); ok {
			t.Errorf("raw(%q) should refuse", bad)
		}
	}
}

func TestTranslateVariables(t *testing.T) {
	render := func(s string, ctx int) (string, []string) {
		parts, bad := translate(s, ctx)
		return tmpl(parts), bad
	}
	cases := []struct {
		in   string
		ctx  int
		want string
	}{
		{"https://$host$request_uri", ctxRequest, `"https://{host}{uri}"`},
		{"https://${host}/x", ctxRequest, `"https://{host}/x"`},
		{"$uri", ctxRequest, `"{path}"`},
		{"$uri", ctxInternal, `"{uri}"`},
		{"$document_uri/", ctxInternal, `"{uri}/"`},
		{"$request_uri", ctxInternal, `"{uri}?{query}"`},
		{"/index.php?$args", ctxInternal, `"/index.php?{query}"`},
		{"$query_string", ctxRequest, `"{query}"`},
		{"$scheme://$http_host", ctxRequest, `"{scheme}://{host}"`},
		{"$request_method $remote_addr $request_id", ctxRequest, `"{method} {remote_ip} {request_id}"`},
		{"$http_user_agent", ctxRequest, `"{header.User-Agent}"`},
		{"$http_x_forwarded_for", ctxRequest, `"{header.X-Forwarded-For}"`},
		{"$arg_page", ctxRequest, `"{query.page}"`},
		{"$cookie_session", ctxRequest, `"{cookie.session}"`},
		{"/p/$1/$2", ctxInternal, `"/p/{re.1}/{re.2}"`},
		{"cost $5 only", ctxRequest, `"cost {re.5} only"`},
		{"a{b}", ctxRequest, `"a\{b\}"`},
		{"no variables", ctxRequest, `"no variables"`},
	}
	for _, c := range cases {
		got, bad := render(c.in, c.ctx)
		if got != c.want || len(bad) > 0 {
			t.Errorf("translate(%q, %d) = %s, %v; want %s", c.in, c.ctx, got, bad, c.want)
		}
	}
	_, bad := translate("$server_name $upstream_addr $uri", ctxRequest)
	if strings.Join(bad, " ") != "$server_name $upstream_addr" {
		t.Errorf("unsupported variables = %v", bad)
	}
}

func TestHeaderName(t *testing.T) {
	for in, want := range map[string]string{"user_agent": "User-Agent", "x_forwarded_for": "X-Forwarded-For", "host": "Host", "ACCEPT_LANGUAGE": "Accept-Language"} {
		if got := headerName(in); got != want {
			t.Errorf("headerName(%q) = %q, want %q", in, got, want)
		}
	}
}
