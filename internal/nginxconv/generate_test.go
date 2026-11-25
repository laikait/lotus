package nginxconv

import (
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The translator must never write a configuration LOTUS refuses, whatever
// Nginx configuration it is given. This builds many random ones from a pool of
// directives (including odd values) and checks every translation.

var locBodies = []string{
	`return 200 "ok";`, `return 301 https://$host$request_uri;`, `return 404;`, `return 302 /x?a=$arg_a&b=$1;`, `return https://other.test/;`, `return 999;`,
	`try_files $uri $uri/ /index.php?$args;`, `try_files $uri =404;`, `try_files $uri @named;`, `try_files $uri;`, `try_files =404;`,
	`proxy_pass http://127.0.0.1:9000;`, `proxy_pass http://app;`, `proxy_pass http://app/;`, `proxy_pass https://b.test:8443/x/;`, `proxy_pass http://$host;`, `proxy_pass http://unix:/run/a.sock:/;`,
	`proxy_set_header Host $host;`, `proxy_set_header X-A "1 2";`, `proxy_set_header X-B $remote_user;`, `proxy_set_header Bad_Header! 1;`, `proxy_set_header Connection "";`,
	`proxy_read_timeout 30s;`, `proxy_connect_timeout 1m;`, `proxy_read_timeout lots;`,
	`fastcgi_pass 127.0.0.1:9001;`, `fastcgi_pass unix:/run/php.sock;`, `fastcgi_pass php;`, `fastcgi_pass $x;`, `fastcgi_read_timeout 2m;`,
	`rewrite ^/a/(.*)$ /b/$1 last;`, `rewrite ^/a$ /b break;`, `rewrite ^/a$ https://x.test/ permanent;`, `rewrite ^/(?!a) /b;`, `rewrite (a /b;`, `rewrite ^/q'uote /b;`, `rewrite ^/a$ /b weird;`,
	`if ($request_method = POST) { return 405; }`, `if ($http_x ~* "a|b") { return 403; }`, `if ($scheme = http) { return 301 https://$host$request_uri; }`,
	`if ($foo) { return 1; }`, `if ($a = b) { set $x 1; }`, `if (-f $request_filename) { return 200; }`,
	`allow 10.0.0.0/8; deny all;`, `deny all;`, `deny 1.2.3.4; allow all;`, `allow 1.2.3.4; deny 5.6.7.8;`, `allow nonsense; deny all;`,
	`limit_req zone=z burst=5;`, `limit_req zone=z burst=3 nodelay;`, `limit_req zone=nope;`,
	`proxy_cache pages;`, `fastcgi_cache pages;`, `proxy_cache nope;`, `proxy_cache off;`, `proxy_cache_valid 200 5m;`, `proxy_cache_valid 404 1m;`, `proxy_cache_valid any 10s;`,
	`proxy_cache_use_stale error timeout;`, `proxy_cache_key $scheme$host$uri;`, `proxy_cache_bypass $cookie_x;`, `proxy_cache_lock_timeout 4s;`,
	`error_page 404 /404.html;`, `error_page 500 502 /50x.html;`, `error_page 404 =200 /x.html;`, `error_page 404 @named;`, `error_page 301 /x;`, `error_page 404 http://x.test/;`,
	`index a.html b.html;`, `index $x;`, `root /other;`, `root /srv/x;`, `alias /data/;`, `internal;`, `expires 1d;`, `auth_basic "x";`, `autoindex on;`,
	`gzip off;`, `gzip on;`, `client_max_body_size 5m;`, `client_max_body_size 0;`, `client_max_body_size bogus;`, `add_header X-A 1;`, `add_header X-A $host;`,
	`access_log off;`, `set $a 1;`, `charset utf-8;`, `fastcgi_param A B;`, `unknown_directive on;`, `include fastcgi_params;`, `sendfile on;`,
}

var locPaths = []string{
	"/", "= /", "/a/", "^~ /static/", `~ \.php$`, `~* \.(png|jpg)$`, "@named", "@Other", "= /old", "/a b/", `~ ^/(a|b)\1$`, "~ (", "/q?x", "relative", `/\{x\}`, "~ \"a'b\"", "/very/long/path/with/many/parts/",
}

var srvBodies = []string{
	"listen 80;", "listen 443 ssl http2;", "listen [::]:80;", "listen 127.0.0.1:8081;", "listen 8443 ssl default_server;", "listen 443 quic reuseport;", "listen unix:/x.sock;", "listen 99999;",
	"server_name a.test www.a.test;", "server_name .b.test;", "server_name _;", `server_name ~^x\d+$;`, "server_name *.c.test;", "server_name \"\";",
	"root /srv/a;", "root /srv/$host;", "index index.php index.html;",
	"ssl_certificate /c.pem; ssl_certificate_key /k.pem;", "ssl_certificate $c; ssl_certificate_key /k.pem;", "ssl_protocols TLSv1.2 TLSv1.3;", "ssl_protocols TLSv1;",
	"return 301 https://$host$request_uri;", "return 444;", "rewrite ^ /x;", "if ($host = x) { return 403; }", "set $a 1;",
	"add_header X-Frame-Options DENY;", "add_header \"Content-Security-Policy\" \"default-src 'self'\" always;", `add_header Bad\ Name 1;`,
	"access_log /var/log/a.log;", "access_log off;", "access_log syslog:server=x;", "error_log /var/log/a.err warn;",
	"gzip on; gzip_types text/css application/json; gzip_min_length 1k; gzip_comp_level 6;", "gzip off;", "gzip on; gzip_types *;", "gzip on; gzip_static on;",
	"limit_req zone=z burst=10;", "client_max_body_size 8m;", "keepalive_timeout 5s;", "error_page 404 /404.html;",
	"allow 10.0.0.0/8; deny all;", "auth_basic \"x\";", "set_real_ip_from 10.0.0.0/8;",
}

var httpBodies = []string{
	"upstream app { server 127.0.0.1:9000 weight=2; server 127.0.0.1:9001 backup; least_conn; }",
	"upstream app { server 10.0.0.1:80; ip_hash; }", "upstream php { server 127.0.0.1:9100; server 127.0.0.1:9101; }",
	"upstream bad { server ; }", "upstream { server 1.2.3.4:5; }",
	"proxy_cache_path /c keys_zone=pages:10m max_size=1g;", "fastcgi_cache_path /f keys_zone=pages:5m;",
	"limit_req_zone $binary_remote_addr zone=z:10m rate=5r/s;", "limit_req_zone $server_name zone=y:1m rate=1r/m;",
	"gzip on;", "client_max_body_size 1m;", "keepalive_timeout 65;", "set_real_ip_from 192.168.0.0/16; real_ip_header X-Forwarded-For;",
	"sendfile on; tcp_nopush on;", "map $uri $x { default 1; }", "include mime.types;", "ssl_protocols TLSv1.3;",
}

func randomConfig(r *rand.Rand) string {
	var b strings.Builder
	b.WriteString("http {\n")
	for i := 0; i < r.Intn(5); i++ {
		b.WriteString(httpBodies[r.Intn(len(httpBodies))] + "\n")
	}
	for s := 0; s < 1+r.Intn(3); s++ {
		b.WriteString("server {\n")
		for i := 0; i < 1+r.Intn(6); i++ {
			b.WriteString(srvBodies[r.Intn(len(srvBodies))] + "\n")
		}
		for l := 0; l < r.Intn(5); l++ {
			writeLocation(&b, r, 0)
		}
		b.WriteString("}\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func writeLocation(b *strings.Builder, r *rand.Rand, depth int) {
	b.WriteString("location " + locPaths[r.Intn(len(locPaths))] + " {\n")
	for i := 0; i < r.Intn(6); i++ {
		b.WriteString(locBodies[r.Intn(len(locBodies))] + "\n")
	}
	if depth < 2 && r.Intn(4) == 0 {
		writeLocation(b, r, depth+1)
	}
	b.WriteString("}\n")
}

func TestRandomConfigurationsTranslateToValidOnes(t *testing.T) {
	n := 400
	if testing.Short() {
		n = 100
	}
	if v, err := strconv.Atoi(os.Getenv("LOTUS_NGINXCONV_CASES")); err == nil {
		n = v
	}
	r := rand.New(rand.NewSource(42))
	dir := t.TempDir()
	var parsed, unparsable int
	for i := 0; i < n; i++ {
		src := randomConfig(r)
		path := filepath.Join(dir, "nginx.conf")
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		nodes, err := Parse(path, Options{})
		if err != nil {
			unparsable++ // some odd values (an unquoted brace) are not Nginx syntax
			continue
		}
		parsed++
		res := Convert(nodes, true)
		func() {
			defer func() {
				if t.Failed() {
					t.Logf("--- the Nginx configuration of case %d ---\n%s", i, src)
				}
			}()
			checkValid(t, res.Config)
		}()
		if t.Failed() {
			return
		}
	}
	if parsed < n/2 {
		t.Errorf("only %d of %d random configurations parsed (%d did not)", parsed, n, unparsable)
	}
}
