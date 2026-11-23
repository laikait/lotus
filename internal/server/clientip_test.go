package server

import (
	"strings"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/config/model"
)

// clientIPSite starts a server whose only route echoes the client IP, with a
// match block that blocks 198.51.100.0/24. trusted lists the proxy networks.
func clientIPSite(t *testing.T, trusted ...string) (*harness, string) {
	t.Helper()
	s := siteConf("a.test", docroot(t, "x"), "http")
	s.Limits.MaxRequestBody = model.DefaultMaxRequestBody
	s.Routes = []model.Route{{
		Kind: model.RoutePrefix, Path: "/", Root: s.Root, Index: s.Index, MaxRequestBody: s.Limits.MaxRequestBody,
		Actions: ret(200, "ip={remote_ip}"),
		Matches: []model.Match{{
			ClientIP: &model.ClientIPCond{Networks: []netipPrefix{mustPrefix("198.51.100.0/24")}},
			Actions:  ret(403, "blocked"),
		}},
	}}
	cfg := baseConfig(s)
	for _, p := range trusted {
		cfg.TrustedProxies = append(cfg.TrustedProxies, mustPrefix(p))
	}
	h := start(t, cfg)
	return h, "http://" + h.addr("http")
}

func waitForLog(h *harness, want string) string {
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(h.stdout.String(), want) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	return h.stdout.String()
}

func TestClientIPFromTrustedProxy(t *testing.T) {
	h, base := clientIPSite(t, "127.0.0.0/8") // the test client connects from loopback

	resp, body := do(t, "GET", base+"/", map[string]string{"X-Forwarded-For": "203.0.113.7"}, "")
	if resp.StatusCode != 200 || strings.TrimSpace(body) != "ip=203.0.113.7" {
		t.Errorf("{remote_ip} = %d %q, want the forwarded client", resp.StatusCode, body)
	}
	// client_ip conditions use the resolved address.
	resp, body = do(t, "GET", base+"/", map[string]string{"X-Forwarded-For": "198.51.100.9"}, "")
	if resp.StatusCode != 403 || strings.TrimSpace(body) != "blocked" {
		t.Errorf("client_ip match on the forwarded address: %d %q", resp.StatusCode, body)
	}
	// A spoofed left entry doesn't hide the real one the proxy appended.
	resp, _ = do(t, "GET", base+"/", map[string]string{"X-Forwarded-For": "1.1.1.1, 198.51.100.9"}, "")
	if resp.StatusCode != 403 {
		t.Errorf("spoofed left entry: %d, want 403", resp.StatusCode)
	}
	// Forwarded is understood as well.
	_, body = do(t, "GET", base+"/", map[string]string{"Forwarded": `for="[2001:db8::5]:1234";proto=https`}, "")
	if strings.TrimSpace(body) != "ip=2001:db8::5" {
		t.Errorf("Forwarded: %q", body)
	}

	if log := waitForLog(h, "203.0.113.7 - -"); !strings.Contains(log, "203.0.113.7 - -") {
		t.Errorf("the access log must record the client, not the proxy:\n%s", log)
	}
}

func TestForwardedHeadersAreIgnoredFromUntrustedPeers(t *testing.T) {
	h, base := clientIPSite(t) // no trusted proxies

	resp, body := do(t, "GET", base+"/", map[string]string{"X-Forwarded-For": "198.51.100.9", "Forwarded": "for=198.51.100.9"}, "")
	if resp.StatusCode != 200 || strings.TrimSpace(body) != "ip=127.0.0.1" {
		t.Errorf("a client must not be able to choose its own address: %d %q", resp.StatusCode, body)
	}
	if log := waitForLog(h, "127.0.0.1 - -"); strings.Contains(log, "198.51.100.9") {
		t.Errorf("spoofed address reached the log:\n%s", log)
	}

	// Same when proxies are trusted, but this peer is not one of them.
	_, base = clientIPSite(t, "10.0.0.0/8")
	_, body = do(t, "GET", base+"/", map[string]string{"X-Forwarded-For": "198.51.100.9"}, "")
	if strings.TrimSpace(body) != "ip=127.0.0.1" {
		t.Errorf("untrusted peer: %q", body)
	}
}
