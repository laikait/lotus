package config

import (
	"strings"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/config/diag"
	"github.com/laikait/lotus/internal/config/model"
)

const cacheServer = `lotus_config_version 1
server {
    listen "http" { address "127.0.0.1:8080" }
    upstream "app" { backend "http://127.0.0.1:9000" }
    php_backend "php" { fastcgi_pass "127.0.0.1:9001" }
%s
    site "example.com" {
        root "/srv/a"
%s
    }
}`

func cacheConf(server, site string) string {
	return portable(strings.Replace(strings.Replace(cacheServer, "%s", server, 1), "%s", site, 1))
}

func TestCompileCacheZonesAndRouteCaches(t *testing.T) {
	c := compile(t, cacheConf(`
    cache "pages" {
        max_size 256mib
        max_entry_size 4mib
    }
    cache "small"`, `
        route "/api/" {
            proxy "app"
            cache "pages" {
                ttl 5m
                negative_ttl 30s
                stale_if_error 1h
                lock_timeout 2s
                bypass_cookies [ "wordpress_logged_in_*", "PHPSESSID" ]
            }
        }
        route ~ '\.php$' {
            php "php"
            cache "small"
        }`))
	if len(c.Caches) != 2 {
		t.Fatalf("zones: %+v", c.Caches)
	}
	if z := c.Caches[0]; z.Name != "pages" || z.MaxSize != 256<<20 || z.MaxEntrySize != 4<<20 {
		t.Errorf("pages: %+v", z)
	}
	if z := c.Caches[1]; z.Name != "small" || z.MaxSize != model.DefaultCacheMaxSize || z.MaxEntrySize != model.DefaultCacheMaxEntrySize {
		t.Errorf("defaults: %+v", z)
	}
	routes := c.Sites[0].Routes
	rc := routes[0].Cache
	if rc == nil || rc.Zone != "pages" || time.Duration(rc.TTL) != 5*time.Minute || time.Duration(rc.NegativeTTL) != 30*time.Second ||
		time.Duration(rc.StaleIfError) != time.Hour || time.Duration(rc.LockTimeout) != 2*time.Second ||
		rc.Cookies != model.CookiesBypassNamed || len(rc.BypassCookies) != 2 || rc.BypassCookies[0] != "wordpress_logged_in_*" {
		t.Errorf("route cache: %+v", rc)
	}
	rc = routes[1].Cache
	if rc == nil || rc.Zone != "small" || rc.TTL != 0 || rc.NegativeTTL != 0 || rc.StaleIfError != 0 ||
		time.Duration(rc.LockTimeout) != model.DefaultCacheLockTimeout || rc.Cookies != model.CookiesBypassAny || len(rc.BypassCookies) != 0 {
		t.Errorf("defaults: %+v", rc)
	}
}

func TestCacheCookieKeywords(t *testing.T) {
	for word, want := range map[string]string{"any": model.CookiesBypassAny, "none": model.CookiesBypassNone} {
		c := compile(t, cacheConf(`cache "pages"`, `route "/" { proxy "app"
 cache "pages" { bypass_cookies `+word+` } }`))
		if got := c.Sites[0].Routes[0].Cache.Cookies; got != want {
			t.Errorf("%s: %q", word, got)
		}
	}
}

func TestRoutesWithoutCacheHaveNone(t *testing.T) {
	c := compile(t, cacheConf(`cache "pages"`, `route "/" { proxy "app" }`))
	if c.Sites[0].Routes[0].Cache != nil {
		t.Errorf("%+v", c.Sites[0].Routes[0].Cache)
	}
	if c := compile(t, cacheConf(``, `route "/" { proxy "app" }`)); len(c.Caches) != 0 {
		t.Errorf("%+v", c.Caches)
	}
}

func TestCacheErrors(t *testing.T) {
	type tc struct{ server, site, code, text string }
	for name, c := range map[string]tc{
		"unknown zone": {`cache "pages"`, `route "/" { proxy "app"
 cache "nope" }`, "", "nope"},
		"static route": {`cache "pages"`, `route "/" { try_files [ "{uri}", "=404" ]
 cache "pages" }`, diag.CodeInvalidValue, "cache needs a route that proxies or runs PHP"},
		"route with nothing to run": {`cache "pages"`, `route "/" { cache "pages" }`, diag.CodeInvalidValue, "proxies or runs PHP"},
		"entry bigger than zone": {`cache "pages" { max_size 2mib
 max_entry_size 3mib }`, `route "/" { proxy "app" }`, diag.CodeInvalidValue, "max_entry_size"},
		"zone too small": {`cache "pages" { max_size 100kib }`, `route "/" { proxy "app" }`, "", "max_size"},
		"zero ttl unit": {`cache "pages"`, `route "/" { proxy "app"
 cache "pages" { ttl 5 } }`, "", "ttl"},
		"negative-looking ttl": {`cache "pages"`, `route "/" { proxy "app"
 cache "pages" { ttl "soon" } }`, "", "ttl"},
		"empty cookie list": {`cache "pages"`, `route "/" { proxy "app"
 cache "pages" { bypass_cookies [] } }`, "", "bypass_cookies"},
		"bad cookie keyword": {`cache "pages"`, `route "/" { proxy "app"
 cache "pages" { bypass_cookies sometimes } }`, "", "bypass_cookies"},
		"zone twice": {`cache "pages"` + "\n" + `cache "pages"`, `route "/" { proxy "app" }`, "", "pages"},
		"cache twice": {`cache "pages"`, `route "/" { proxy "app"
 cache "pages"
 cache "pages" }`, "", "cache"},
		"unknown setting": {`cache "pages" { evict_after 5m }`, `route "/" { proxy "app" }`, "", "evict_after"},
	} {
		_, diags := Check("main.conf", optsFor(map[string]string{"main.conf": cacheConf("    "+c.server, c.site)}))
		out := format(diags)
		if len(errorsOf(diags)) == 0 || (c.code != "" && !strings.Contains(out, c.code)) || !strings.Contains(out, c.text) {
			t.Errorf("%s: want %s %q in:\n%s", name, c.code, c.text, out)
		}
	}
}

func TestCacheIsAdminOnly(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"zone in a tenant file": {
			"main.conf":   cacheConf("", `include "tenant.conf"`),
			"tenant.conf": `cache "mine"`,
		},
		"cache in a tenant route": {
			"main.conf":   cacheConf(`cache "pages"`, `include "tenant.conf"`),
			"tenant.conf": "route \"/x\" { proxy \"app\"\n cache \"pages\" }",
		},
	} {
		_, diags := Check("main.conf", optsFor(files))
		if len(errorsOf(diags)) == 0 {
			t.Errorf("%s: a tenant file used the cache", name)
		}
	}
}
