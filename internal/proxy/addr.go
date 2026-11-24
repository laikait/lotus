package proxy

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ParseBackend reads the address of a backend as written in the configuration:
//
//	http://host[:port]     plain HTTP (port 80 by default)
//	https://host[:port]    HTTP over TLS (port 443 by default)
//	unix:/path/to/socket   HTTP over a Unix socket
//
// A path, query or credentials in the address are refused: where a request goes
// on the backend comes from the request (and the route's rewrites), not from here.
func ParseBackend(s string) (BackendSpec, error) {
	if rest, ok := strings.CutPrefix(s, "unix:"); ok {
		if rest == "" || strings.ContainsRune(rest, 0) {
			return BackendSpec{}, fmt.Errorf("%q: a unix backend needs a socket path (unix:/run/app.sock)", s)
		}
		return BackendSpec{Network: "unix", Address: rest}, nil
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return BackendSpec{}, fmt.Errorf("%q is not a backend address; use http://host:port, https://host:port or unix:/path", s)
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return BackendSpec{}, fmt.Errorf("%q: a backend address is only a scheme, host and port; the path of a request is kept (use a rewrite to change it)", s)
	}
	host, port := u.Hostname(), u.Port()
	if host == "" {
		return BackendSpec{}, fmt.Errorf("%q has no host", s)
	}
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return BackendSpec{}, fmt.Errorf("%q has an invalid port", s)
	}
	return BackendSpec{Network: "tcp", Address: net.JoinHostPort(host, port), TLS: u.Scheme == "https", Host: host}, nil
}
