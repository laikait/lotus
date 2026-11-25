package nginxconv

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/laikait/lotus/internal/config"
	"github.com/laikait/lotus/internal/config/diag"
	"github.com/laikait/lotus/internal/config/loader"
)

// write puts the files in a temp directory and returns the path of the main
// configuration, nginx.conf.
func write(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "nginx.conf")
}

// convert parses and converts, and checks that the result is a valid LOTUS
// configuration: the translator must never write something LOTUS refuses.
func convert(t *testing.T, conf string, extra ...map[string]string) *Result {
	t.Helper()
	files := map[string]string{"nginx.conf": conf}
	for _, m := range extra {
		for k, v := range m {
			files[k] = v
		}
	}
	nodes, err := Parse(write(t, files), Options{})
	if err != nil {
		t.Fatal(err)
	}
	res := Convert(nodes, false)
	checkValid(t, res.Config)
	return res
}

func checkValid(t *testing.T, config_ string) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "out.lotus")
	if err := os.WriteFile(out, []byte(config_), 0o600); err != nil {
		t.Fatal(err)
	}
	if r, diags := config.Check(out, loader.Options{InsecureIncludes: true}); r == nil {
		var b strings.Builder
		for _, d := range diags {
			if d.Severity == diag.Error {
				b.WriteString(d.Message + " (line " + strconv.Itoa(d.Pos.Line) + ")\n")
			}
		}
		t.Fatalf("the translation is not a valid LOTUS configuration:\n%s\n--- output ---\n%s", b.String(), config_)
	}
}

// server wraps directives in an http block with one server on port 8080.
func serverConf(body string) string {
	return "http {\n server {\n  listen 8080;\n  server_name x.test;\n  root /srv/x;\n" + body + "\n }\n}\n"
}

func has(t *testing.T, res *Result, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(res.Config, w) {
			t.Errorf("the output lacks %q:\n%s", w, res.Config)
		}
	}
}

func lacks(t *testing.T, res *Result, bad ...string) {
	t.Helper()
	for _, w := range bad {
		if strings.Contains(res.Config, w) {
			t.Errorf("the output has %q:\n%s", w, res.Config)
		}
	}
}

// issue finds the first issue of a directive with that status whose message
// contains part.
func issue(res *Result, directive string, st Status, part string) *Issue {
	for i := range res.Issues {
		if res.Issues[i].Directive == directive && res.Issues[i].Status == st && strings.Contains(res.Issues[i].Message, part) {
			return &res.Issues[i]
		}
	}
	return nil
}

func wantIssue(t *testing.T, res *Result, directive string, st Status, part string) {
	t.Helper()
	if issue(res, directive, st, part) == nil {
		t.Errorf("no %s issue for %s mentioning %q; issues: %+v", st, directive, part, res.Issues)
	}
}

func noIssue(t *testing.T, res *Result, directive string) {
	t.Helper()
	for _, i := range res.Issues {
		if i.Directive == directive && (i.Status == Unsupported || i.Status == Approximated) {
			t.Errorf("unexpected %s issue for %s: %s", i.Status, directive, i.Message)
		}
	}
}

const sample = `
user www-data;
worker_processes auto;
events { worker_connections 1024; }
http {
    include mime.types;
    sendfile on;
    keepalive_timeout 65;
    client_max_body_size 20m;
    gzip on;
    gzip_types text/css application/json application/javascript;
    gzip_min_length 1k;
    gzip_comp_level 5;

    upstream app {
        least_conn;
        server 127.0.0.1:9001 weight=2;
        server 127.0.0.1:9002 max_fails=3 fail_timeout=30s;
    }
    proxy_cache_path /var/cache/nginx keys_zone=pages:10m max_size=1g;

    server {
        listen 80;
        listen [::]:80;
        server_name example.com www.example.com;
        return 301 https://$host$request_uri;
    }

    server {
        listen 443 ssl http2;
        listen [::]:443 ssl http2;
        server_name example.com www.example.com;
        ssl_certificate /etc/ssl/example.pem;
        ssl_certificate_key /etc/ssl/example.key;
        ssl_protocols TLSv1.2 TLSv1.3;
        root /var/www/example;
        index index.php index.html;
        error_page 404 /404.html;
        add_header X-Frame-Options DENY;

        location / {
            try_files $uri $uri/ /index.php?$args;
        }
        location ~ \.php$ {
            try_files $uri =404;
            fastcgi_pass unix:/run/php/php8.2-fpm.sock;
            include fastcgi_params;
            fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        }
        location ^~ /uploads/ {
            deny all;
        }
        location /api/ {
            proxy_pass http://app;
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_cache pages;
            proxy_cache_valid 200 10m;
            proxy_cache_valid 404 1m;
        }
        location = /old {
            return 301 /new;
        }
        location /blog/ {
            rewrite ^/blog/(\d+)/(.*)$ /posts/$2.html last;
        }
    }
}
`

func TestTheWholeSample(t *testing.T) {
	res := convert(t, sample)
	has(t, res,
		`listen "http-80"`, `address "[::]:80"`, `listen "https-443"`, `protocol https`,
		`min_version "1.2"`, `max_request_body 20mib`, `idle_timeout 1m5s`,
		`algorithms [ "gzip" ]`, `types [ "text/html", "text/css", "application/json", "application/javascript", "text/javascript" ]`,
		`min_size 1kib`, `level default`,
		`upstream "app"`, `balance least_conn`, `weight 2`, `max_fails 3`, `fail_timeout 30s`,
		`php_backend "php-unix-run-php-php8-2-fpm-sock"`, `fastcgi_pass "unix:/run/php/php8.2-fpm.sock"`,
		`cache "pages"`, `max_size 1gib`,
		`site "example.com"`, `aliases [ "www.example.com" ]`, `tls manual`, `certificate "/etc/ssl/example.pem"`,
		`index [ "index.php", "index.html" ]`, `error_page 404 "/404.html"`, `"X-Frame-Options" = "DENY"`,
		`try_files [ "{uri}", "{uri}/", "/index.php?{query}" ]`,
		`route ~ '\.php$'`, `php "php-unix-run-php-php8-2-fpm-sock"`,
		`route ^~ "/uploads/"`, `return 403`,
		`route "/api/"`, `proxy "app"`, `ttl 10m`, `negative_ttl 1m`,
		`route = "/old"`, `return 301 "/new"`,
		`rewrite '^/blog/(\d+)/(.*)$' "/posts/{re.2}.html"`,
	)
	// The port 80 server became redirect matches on the https site.
	if strings.Count(res.Config, `site "`) != 1 {
		t.Errorf("one site expected:\n%s", res.Config)
	}
	has(t, res, `scheme "http"`, `return 301 "https://{host}{uri}"`)
	lacks(t, res, "try_files [ \"{uri}\", \"=404\" ]") // dropped: php answers 404 itself
	if res.Count(Unsupported) != 0 {
		t.Errorf("nothing in the sample is unsupported: %+v", res.Issues)
	}
	wantIssue(t, res, "proxy_cache_path", Approximated, "memory")
}

func TestStaticSite(t *testing.T) {
	res := convert(t, serverConf(`
        index home.html index.html;
        error_page 404 /e404.html;
        error_page 500 502 503 504 /50x.html;
        access_log /var/log/x.log;
        error_log /var/log/x.err warn;
        add_header X-Content-Type-Options nosniff;
        add_header "Referrer-Policy" "no-referrer" always;
    `))
	has(t, res, `listen "http-8080"`, `address "0.0.0.0:8080"`, `site "x.test"`, `listeners [ "http-8080" ]`, `tls off`,
		`root "/srv/x"`, `index [ "home.html", "index.html" ]`, `error_page 404 "/e404.html"`,
		`error_page 500 "/50x.html"`, `error_page 504 "/50x.html"`,
		`access "/var/log/x.log"`, `error "/var/log/x.err"`,
		`"X-Content-Type-Options" = "nosniff"`, `"Referrer-Policy" = "no-referrer"`)
	if res.NeedsReview() {
		t.Errorf("nothing to review: %+v", res.Issues)
	}
}

func TestLogsOff(t *testing.T) {
	res := convert(t, serverConf(`access_log off;`))
	has(t, res, `access null`)
	res = convert(t, serverConf(`access_log /var/log/a.log main buffer=32k;`))
	has(t, res, `access "/var/log/a.log"`)
	wantIssue(t, res, "access_log", Ignored, "access-log format")
	res = convert(t, serverConf(`access_log syslog:server=unix:/dev/log;`))
	wantIssue(t, res, "access_log", Unsupported, "syslog")
}

func TestServerNames(t *testing.T) {
	res := convert(t, `http { server { listen 80; server_name .example.com *.cdn.example.com ~^img\d+\.example\.com$ www.*; } }`)
	has(t, res, `site "example.com"`, `aliases [ "*.example.com", "*.cdn.example.com" ]`)
	wantIssue(t, res, "server_name", Unsupported, "regular expression")
	wantIssue(t, res, "server_name", Unsupported, `"www.*"`)

	res = convert(t, `http { server { listen 80; server_name Example.COM; } }`)
	has(t, res, `site "example.com"`)

	res = convert(t, `http { server { listen 80; server_name _; root /srv; } }`)
	has(t, res, `site "default.invalid"`)
	wantIssue(t, res, "server", Approximated, "default.invalid")
}

func TestDuplicateNamesKeepTheFirst(t *testing.T) {
	res := convert(t, `http {
        server { listen 80; server_name a.test; root /one; }
        server { listen 81; server_name a.test; root /two; }
    }`)
	has(t, res, `root "/one"`)
	lacks(t, res, `root "/two"`)
	wantIssue(t, res, "server", Unsupported, "one site per name")
}

func TestListeners(t *testing.T) {
	res := convert(t, `http {
        server { listen 127.0.0.1:8081; listen *:8082; listen 8083; listen localhost:8084; server_name a.test; }
    }`)
	has(t, res, `address "127.0.0.1:8081"`, `address "0.0.0.0:8082"`, `address "0.0.0.0:8083"`, `address "localhost:8084"`)

	// IPv4 and IPv6 wildcards of one port become one dual-stack listener.
	res = convert(t, `http { server { listen 80; listen [::]:80; server_name a.test; } server { listen [::]:80; server_name b.test; } }`)
	if strings.Count(res.Config, `listen "http-80"`) != 1 || !strings.Contains(res.Config, `address "[::]:80"`) {
		t.Errorf("one dual-stack listener expected:\n%s", res.Config)
	}
	wantIssue(t, res, "listen", Approximated, "dual-stack")

	res = convert(t, `http { server { listen unix:/run/x.sock; listen 8085 proxy_protocol; server_name a.test; } }`)
	wantIssue(t, res, "listen", Unsupported, "unix socket")
	wantIssue(t, res, "listen", Unsupported, "PROXY protocol")

	res = convert(t, `http { server { server_name a.test; } }`)
	has(t, res, `address "0.0.0.0:80"`)
	wantIssue(t, res, "server", Approximated, "port 80")
}

func TestDefaultServerComesFirst(t *testing.T) {
	res := convert(t, `http {
        server { listen 80; server_name a.test; }
        server { listen 80 default_server; server_name b.test; }
    }`)
	if strings.Index(res.Config, `site "b.test"`) > strings.Index(res.Config, `site "a.test"`) {
		t.Errorf("the default server must be the first site:\n%s", res.Config)
	}
}

func TestHTTP3(t *testing.T) {
	res := convert(t, `http {
        server {
            listen 443 ssl; listen 443 quic reuseport; server_name a.test;
            ssl_certificate /c.pem; ssl_certificate_key /k.pem;
        }
    }`)
	has(t, res, "http3 true")
	res = convert(t, `http { server { listen 80; listen 443 quic; server_name a.test; ssl_certificate /c.pem; ssl_certificate_key /k.pem; } }`)
	has(t, res, "http3 true")
}

func TestTLS(t *testing.T) {
	res := convert(t, `http { server { listen 443 ssl; server_name a.test; ssl_certificate /c.pem; ssl_certificate_key /k.pem; ssl_protocols TLSv1.3; } }`)
	has(t, res, "tls manual", `certificate "/c.pem"`, `certificate_key "/k.pem"`, `min_version "1.3"`)

	res = convert(t, `http { server { listen 443 ssl; server_name a.test; } }`)
	has(t, res, "tls manual", "CHANGE-ME-fullchain.pem")
	wantIssue(t, res, "server", Unsupported, "names no certificate")

	res = convert(t, `http { server { listen 443 ssl; server_name a.test; ssl_certificate /etc/$host.pem; ssl_certificate_key /k.pem; } }`)
	wantIssue(t, res, "ssl_certificate", Unsupported, "variables")
	has(t, res, "CHANGE-ME")

	res = convert(t, `http { server { listen 443 ssl; server_name a.test; ssl_certificate /c.pem; ssl_certificate_key /k.pem; ssl_protocols TLSv1 TLSv1.2; } }`)
	has(t, res, `min_version "1.2"`)
	wantIssue(t, res, "ssl_protocols", Approximated, "TLS 1.2 and 1.3 only")

	// Inherited from http.
	res = convert(t, `http { ssl_certificate /c.pem; ssl_certificate_key /k.pem; server { listen 443 ssl; server_name a.test; } }`)
	has(t, res, "tls manual")
}

func TestUpstreams(t *testing.T) {
	res := convert(t, `http {
        upstream app {
            ip_hash;
            server 10.0.0.1:8080 weight=3 max_conns=20;
            server 10.0.0.2;
            server unix:/run/app.sock;
            server 10.0.0.9:8080 down;
            server 10.0.0.4:8080 backup;
            keepalive 32;
        }
        server { listen 80; server_name a.test;
            location / { proxy_pass http://app; }
        }
    }`)
	has(t, res, `upstream "app"`, `balance ip_hash`, `backend "http://10.0.0.1:8080"`, `weight 3`, `max_conns 20`,
		`backend "http://10.0.0.2:80"`, `backend "unix:/run/app.sock"`, `backend "http://10.0.0.4:8080"`)
	lacks(t, res, "10.0.0.9")
	wantIssue(t, res, "server", Approximated, "backup")
	wantIssue(t, res, "server", Ignored, "down")
	wantIssue(t, res, "keepalive", Ignored, "")
}

func TestHashByURI(t *testing.T) {
	res := convert(t, `http { upstream a { hash $request_uri consistent; server 10.0.0.1:80; } server { listen 80; server_name a.test; location / { proxy_pass http://a; } } }`)
	has(t, res, "balance uri_hash")
	res = convert(t, `http { upstream a { hash $cookie_sid; server 10.0.0.1:80; } server { listen 80; server_name a.test; location / { proxy_pass http://a; } } }`)
	wantIssue(t, res, "hash", Approximated, "uri_hash")
}

func TestProxyPass(t *testing.T) {
	// A direct backend becomes an upstream of its own; Nginx sends the
	// backend's Host unless told otherwise.
	res := convert(t, serverConf(`location /app/ { proxy_pass http://127.0.0.1:9000; }`))
	has(t, res, `upstream "proxy-127-0-0-1-9000"`, `backend "http://127.0.0.1:9000"`, `proxy "proxy-127-0-0-1-9000"`, "proxy_host upstream")
	noIssue(t, res, "proxy_pass")

	res = convert(t, serverConf(`location /app/ { proxy_pass https://backend.internal; proxy_set_header Host $host; }`))
	has(t, res, `backend "https://backend.internal:443"`)
	lacks(t, res, "proxy_host")

	res = convert(t, serverConf(`location /app/ { proxy_pass http://127.0.0.1:9000; proxy_set_header Host $proxy_host; }`))
	has(t, res, "proxy_host upstream")

	// The URI part replaces the matched prefix: only an identical one is a no-op.
	res = convert(t, serverConf(`location /app/ { proxy_pass http://127.0.0.1:9000/app/; }`))
	noIssue(t, res, "proxy_pass")
	res = convert(t, serverConf(`location /app/ { proxy_pass http://127.0.0.1:9000/; }`))
	wantIssue(t, res, "proxy_pass", Approximated, "replaces the matching part")
	res = convert(t, serverConf(`location / { proxy_pass http://127.0.0.1:9000/; }`))
	noIssue(t, res, "proxy_pass")

	res = convert(t, serverConf(`location /a/ { proxy_pass http://$host; }`))
	wantIssue(t, res, "proxy_pass", Unsupported, "variables")
	lacks(t, res, "proxy ")

	res = convert(t, serverConf(`location /a/ { proxy_pass http://unix:/run/app.sock:/; }`))
	has(t, res, `backend "unix:/run/app.sock"`)
}

func TestProxyHeaders(t *testing.T) {
	res := convert(t, serverConf(`location /a/ {
        proxy_pass http://127.0.0.1:9000;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header X-Env production;
        proxy_set_header X-User $remote_user;
    }`))
	has(t, res, `proxy_set_header "X-Env" "production"`)
	lacks(t, res, "X-Real-IP", "X-Forwarded", "Upgrade")
	wantIssue(t, res, "proxy_set_header", Ignored, "sets X-Real-IP itself")
	wantIssue(t, res, "proxy_set_header", Ignored, "WebSocket")
	wantIssue(t, res, "proxy_set_header", Unsupported, "variables")
}

func TestProxyTimeouts(t *testing.T) {
	res := convert(t, serverConf(`location /a/ { proxy_pass http://127.0.0.1:9000; proxy_connect_timeout 3s; proxy_read_timeout 2m; }`))
	has(t, res, "connect_timeout 3s", "response_timeout 2m")
	// A second location sharing the backend with another value keeps the first.
	res = convert(t, serverConf(`
        location /a/ { proxy_pass http://127.0.0.1:9000; proxy_read_timeout 10s; }
        location /b/ { proxy_pass http://127.0.0.1:9000; proxy_read_timeout 90s; }`))
	has(t, res, "response_timeout 10s")
	wantIssue(t, res, "proxy_read_timeout", Approximated, "shared")
}

func TestFastCGI(t *testing.T) {
	res := convert(t, serverConf(`location ~ \.php(/|$) { fastcgi_pass 127.0.0.1:9000; fastcgi_read_timeout 120s; fastcgi_index index.php; fastcgi_split_path_info ^(.+\.php)(/.+)$; }`))
	has(t, res, `php_backend "php-127-0-0-1-9000"`, `fastcgi_pass "127.0.0.1:9000"`, "timeout 2m", `route ~ '\.php(/|$)'`, `php "php-127-0-0-1-9000"`)
	if res.NeedsReview() {
		t.Errorf("a plain PHP location is fully translated: %+v", res.Issues)
	}

	// Two locations, one backend.
	res = convert(t, serverConf(`location ~ \.php$ { fastcgi_pass 127.0.0.1:9000; } location /legacy/ { fastcgi_pass 127.0.0.1:9000; }`))
	if strings.Count(res.Config, "php_backend ") != 1 {
		t.Errorf("one backend expected:\n%s", res.Config)
	}

	// A FastCGI upstream.
	res = convert(t, `http { upstream php { server 127.0.0.1:9000; server 127.0.0.1:9001; } server { listen 80; server_name a.test; location ~ \.php$ { fastcgi_pass php; } } }`)
	has(t, res, `fastcgi_pass "127.0.0.1:9000"`)
	wantIssue(t, res, "fastcgi_pass", Approximated, "several servers")

	// try_files next to fastcgi_pass: only the usual guard can be dropped.
	res = convert(t, serverConf(`location ~ \.php$ { try_files $uri /index.php; fastcgi_pass 127.0.0.1:9000; }`))
	wantIssue(t, res, "try_files", Unsupported, "fastcgi_pass")
	wantIssue(t, res, "try_files", Unsupported, "fastcgi_pass")

	res = convert(t, serverConf(`location ~ \.php$ { fastcgi_pass $backend; }`))
	wantIssue(t, res, "fastcgi_pass", Unsupported, "variables")
}

func TestTryFiles(t *testing.T) {
	res := convert(t, serverConf(`
        location / { try_files $uri $uri/ /index.php?$args; }
        location /static/ { try_files $uri =404; }
        location /app/ { try_files $uri @backend; }
        location @backend { return 502; }
    `))
	has(t, res, `try_files [ "{uri}", "{uri}/", "/index.php?{query}" ]`, `try_files [ "{uri}", "=404" ]`,
		`try_files [ "{uri}", "@backend" ]`, `route "@backend"`)
	noIssue(t, res, "try_files")

	res = convert(t, serverConf(`location / { try_files $uri $request_uri; }`))
	has(t, res, `"{uri}?{query}"`)

	res = convert(t, serverConf(`location / { try_files $uri $server_name; }`))
	wantIssue(t, res, "try_files", Unsupported, "$server_name")

	res = convert(t, serverConf(`location / { try_files $uri =abc; }`))
	wantIssue(t, res, "try_files", Unsupported, "not a status")

	// A route either proxies or serves files.
	res = convert(t, serverConf(`location / { try_files $uri @x; proxy_pass http://127.0.0.1:9000; }`))
	wantIssue(t, res, "try_files", Unsupported, "proxies or serves files")
	has(t, res, "proxy ")
}

func TestRewrite(t *testing.T) {
	res := convert(t, serverConf(`
        location /a/ { rewrite ^/a/(.*)$ /b/$1; }
        location /c/ { rewrite ^/c/(.*)$ /d/$1 last; }
        location /e/ { rewrite ^/e/(.*)$ /f/$1 permanent; }
        location /g/ { rewrite ^/g/(.*)$ https://other.test/$1; }
        location /h/ { rewrite ^/h/(.*)$ /i/$1 redirect; }
    `))
	has(t, res, `rewrite '^/a/(.*)$' "/b/{re.1}"`, `rewrite '^/c/(.*)$' "/d/{re.1}"`,
		`rewrite '^/e/(.*)$' "/f/{re.1}" permanent`, `rewrite '^/g/(.*)$' "https://other.test/{re.1}" redirect`,
		`rewrite '^/h/(.*)$' "/i/{re.1}" redirect`)
	noIssue(t, res, "rewrite")

	res = convert(t, serverConf(`location /a/ { rewrite ^/a/(.*)$ /b/$1 break; }`))
	wantIssue(t, res, "rewrite", Approximated, "break")

	res = convert(t, serverConf(`location /a/ { rewrite ^/(?!x)(.*)$ /b/$1; }`))
	wantIssue(t, res, "rewrite", Unsupported, "RE2")

	res = convert(t, serverConf(`rewrite ^/a$ /b;`))
	wantIssue(t, res, "rewrite", Unsupported, "outside a location")
}

func TestReturn(t *testing.T) {
	res := convert(t, serverConf(`
        location = /a { return 301 https://$host$request_uri; }
        location = /b { return 200 "hello {world}"; }
        location = /c { return 404; }
        location = /d { return https://other.test/; }
        location = /e { return 302 /x?id=$arg_id; }
        location = /f { return 301 /$uri; }
    `))
	has(t, res, `return 301 "https://{host}{uri}"`, `return 200 "hello \{world\}"`, "return 404", `return 302 "https://other.test/"`,
		`return 302 "/x?id={query.id}"`, `return 301 "/{path}"`)
	noIssue(t, res, "return")

	res = convert(t, serverConf(`location = /a { return 200 $server_name; }`))
	wantIssue(t, res, "return", Unsupported, "")

	// Nothing after a return runs in Nginx.
	res = convert(t, serverConf(`location /a/ { return 410; proxy_pass http://127.0.0.1:9000; }`))
	lacks(t, res, "proxy ")
	wantIssue(t, res, "location", Ignored, "never runs")
}

func TestServerLevelReturn(t *testing.T) {
	res := convert(t, `http { server { listen 80; server_name a.test; return 444; } }`)
	has(t, res, `route "/"`, "return 444")
	noIssue(t, res, "return")

	res = convert(t, `http { server { listen 80; server_name a.test; location /x/ { return 200 "x"; } return 503; } }`)
	wantIssue(t, res, "location", Ignored, "never runs")
	lacks(t, res, `route "/x/"`)
}

func TestRedirectServers(t *testing.T) {
	// A catch-all port 80 server that redirects to a fixed address.
	res := convert(t, `http {
        server { listen 80 default_server; server_name _; return 301 https://$host$request_uri; }
        server { listen 443 ssl; server_name a.test; ssl_certificate /c.pem; ssl_certificate_key /k.pem; }
    }`)
	has(t, res, `site "default.invalid"`, `listeners [ "http-80" ]`, `return 301 "https://{host}{uri}"`, `site "a.test"`)

	// A location / that only returns.
	res = convert(t, `http {
        server { listen 80; server_name a.test; location / { return 301 https://$host$request_uri; } location /.well-known/acme-challenge/ { root /var/www/acme; } }
        server { listen 443 ssl; server_name a.test; ssl_certificate /c.pem; ssl_certificate_key /k.pem; location /x/ { return 200 "x"; } }
    }`)
	if strings.Count(res.Config, `site "`) != 1 {
		t.Errorf("merged into one site:\n%s", res.Config)
	}
	// The https site has no "/" route: one is added so every path is covered.
	has(t, res, `route "/" {`, `scheme "http"`)

	// A redirect to a different host is not merged.
	res = convert(t, `http {
        server { listen 80; server_name old.test; return 301 https://new.test$request_uri; }
        server { listen 443 ssl; server_name new.test; ssl_certificate /c.pem; ssl_certificate_key /k.pem; }
    }`)
	if strings.Count(res.Config, `site "`) != 2 {
		t.Errorf("two sites expected:\n%s", res.Config)
	}
	has(t, res, `return 301 "https://new.test{uri}"`)
}

func TestIf(t *testing.T) {
	res := convert(t, serverConf(`location /admin/ {
        if ($request_method = POST) { return 405; }
        if ($http_user_agent ~* "bot|crawl") { return 403; }
        if ($scheme = http) { return 301 https://$host$request_uri; }
        if ($arg_debug = 1) { return 404; }
        if ($cookie_session !~ "^[a-f0-9]+$") { return 401; }
        if ($remote_addr = 10.1.2.3) { return 403; }
        if ($host = other.test) { return 410; }
    }`))
	has(t, res, `method [ "POST" ]`, `header "User-Agent" ~* 'bot|crawl'`, `scheme "http"`, `query "debug" = '1'`,
		`cookie "session" !~ '^[a-f0-9]+$'`, `client_ip [ "10.1.2.3/32" ]`, `host [ "other.test" ]`)
	noIssue(t, res, "if")

	for name, cond := range map[string]string{
		"not equal":   `if ($request_method != GET) { return 405; }`,
		"file test":   `if (-f $request_filename) { return 200; }`,
		"two actions": `if ($request_method = POST) { return 405; rewrite ^ /x; }`,
		"variable":    `if ($request_uri ~ "x") { return 403; }`,
		"bad method":  `if ($request_method = BREW) { return 418; }`,
		"bare":        `if ($foo) { return 403; }`,
		"not equal h": `if ($http_x != 1) { return 403; }`,
		"pcre":        `if ($http_x ~ "(?!a)") { return 403; }`,
	} {
		res := convert(t, serverConf("location /a/ { "+cond+" }"))
		if issue(res, "if", Unsupported, "") == nil {
			t.Errorf("%s: %q should be unsupported: %+v", name, cond, res.Issues)
		}
	}
}

func TestAccessRules(t *testing.T) {
	res := convert(t, serverConf(`location /a/ { allow 192.168.1.0/24; allow 10.0.0.1; deny all; }`))
	has(t, res, `client_ip !~ [ "192.168.1.0/24", "10.0.0.1/32" ]`, "return 403")
	noIssue(t, res, "allow")

	res = convert(t, serverConf(`location /a/ { deny 203.0.113.0/24; deny 2001:db8::1; allow all; }`))
	has(t, res, `client_ip = [ "203.0.113.0/24", "2001:db8::1/128" ]`, "return 403")

	res = convert(t, serverConf(`location ~ /\.ht { deny all; }`))
	has(t, res, `route ~ '/\.ht'`, "return 403")

	res = convert(t, serverConf(`location /a/ { allow all; }`))
	lacks(t, res, "client_ip")

	res = convert(t, serverConf(`location /a/ { allow 10.0.0.1; deny 10.0.0.2; allow all; }`))
	wantIssue(t, res, "allow", Unsupported, "can be translated")

	res = convert(t, serverConf(`location /a/ { allow 10.0.0.1; allow banana; deny all; }`))
	wantIssue(t, res, "allow", Unsupported, "can be translated")
}

func TestRateLimit(t *testing.T) {
	res := convert(t, `http {
        limit_req_zone $binary_remote_addr zone=api:10m rate=5r/s;
        limit_req_zone $binary_remote_addr zone=slow:10m rate=30r/m;
        limit_req_status 429;
        server { listen 80; server_name a.test; root /srv;
            limit_req zone=slow burst=10 nodelay;
            location /api/ { limit_req zone=api; }
        }
    }`)
	has(t, res, "requests 30", "per 1m", "burst 11", "requests 5", "per 1s", "burst 1")
	noIssue(t, res, "limit_req")

	// Without nodelay, Nginx queues the burst and answers 503 over the limit.
	res = convert(t, `http {
        limit_req_zone $binary_remote_addr zone=api:10m rate=5r/s;
        server { listen 80; server_name a.test; location /api/ { limit_req zone=api burst=20; } }
    }`)
	wantIssue(t, res, "limit_req", Approximated, "429")

	res = convert(t, `http {
        limit_req_zone $server_name zone=api:10m rate=5r/s;
        server { listen 80; server_name a.test; location /api/ { limit_req zone=api; } }
    }`)
	wantIssue(t, res, "limit_req_zone", Unsupported, "client address")
	wantIssue(t, res, "limit_req", Unsupported, "not defined")
}

func TestCache(t *testing.T) {
	res := convert(t, `http {
        proxy_cache_path /c keys_zone=pages:10m max_size=500m;
        fastcgi_cache_path /f keys_zone=php:5m;
        server { listen 80; server_name a.test;
            location /api/ {
                proxy_pass http://127.0.0.1:9000;
                proxy_cache pages;
                proxy_cache_valid 200 301 10m;
                proxy_cache_valid 404 410 30s;
                proxy_cache_use_stale error timeout http_503 updating;
                proxy_cache_lock on;
                proxy_cache_lock_timeout 3s;
                proxy_cache_key $scheme$proxy_host$request_uri;
            }
            location ~ \.php$ {
                fastcgi_pass 127.0.0.1:9001;
                fastcgi_cache php;
                fastcgi_cache_valid 5m;
            }
        }
    }`)
	has(t, res, `cache "pages" {`, "max_size 500mib", `cache "php"`, "ttl 10m", "negative_ttl 30s", "stale_if_error 1h", "lock_timeout 3s", "ttl 5m")
	wantIssue(t, res, "proxy_cache_use_stale", Approximated, "without a time limit")
	noIssue(t, res, "proxy_cache_key")

	res = convert(t, `http {
        proxy_cache_path /c keys_zone=pages:10m;
        server { listen 80; server_name a.test;
            location /api/ {
                proxy_pass http://127.0.0.1:9000;
                proxy_cache pages;
                proxy_cache_key "$host$uri$cookie_lang";
                proxy_cache_bypass $http_pragma;
                proxy_no_cache $http_authorization;
                proxy_cache_valid 302 1m;
            }
        }
    }`)
	wantIssue(t, res, "proxy_cache_key", Unsupported, "custom cache key")
	wantIssue(t, res, "proxy_cache_bypass", Unsupported, "cookies")
	wantIssue(t, res, "proxy_no_cache", Unsupported, "cookies")
	wantIssue(t, res, "proxy_cache_valid", Approximated, "negative_ttl")

	// A cache needs a proxy or PHP, and a known zone.
	res = convert(t, `http {
        proxy_cache_path /c keys_zone=pages:10m;
        server { listen 80; server_name a.test; location /s/ { proxy_cache pages; } location /t/ { proxy_pass http://127.0.0.1:9000; proxy_cache nozone; } location /u/ { proxy_pass http://127.0.0.1:9000; proxy_cache off; } }
    }`)
	wantIssue(t, res, "proxy_cache", Unsupported, "neither")
	wantIssue(t, res, "proxy_cache", Unsupported, "nozone")
}

func TestCompression(t *testing.T) {
	// gzip on with nothing else: Nginx compresses text/html above 20 bytes at level 1.
	res := convert(t, `http { gzip on; server { listen 80; server_name a.test; } }`)
	has(t, res, `algorithms [ "gzip" ]`, `types [ "text/html" ]`, "min_size 20b", "level fast")

	res = convert(t, `http { gzip on; gzip_static on; gzip_comp_level 9; server { listen 80; server_name a.test; } }`)
	has(t, res, "level best", "precompressed true")

	// A server can turn it off, or change it.
	res = convert(t, `http { gzip on; server { listen 80; server_name a.test; gzip off; } server { listen 81; server_name b.test; gzip_min_length 1k; } }`)
	has(t, res, "enabled false", "min_size 1kib")

	res = convert(t, `http { server { listen 80; server_name a.test; gzip on; gzip_types text/css; } }`)
	has(t, res, `site "a.test"`, "compression {", `types [ "text/html", "text/css" ]`)
	if strings.Contains(res.Config, "\n    compression {") {
		t.Errorf("no server-wide compression expected:\n%s", res.Config)
	}

	res = convert(t, `http { gzip on; gzip_types *; server { listen 80; server_name a.test; } }`)
	wantIssue(t, res, "gzip_types", Approximated, "every type")
	lacks(t, res, "types [")

	// It can't be switched per location.
	res = convert(t, `http { gzip on; server { listen 80; server_name a.test; location /img/ { gzip off; } } }`)
	wantIssue(t, res, "gzip", Unsupported, "per location")
}

func TestLimitsAndTimeouts(t *testing.T) {
	res := convert(t, `http { client_max_body_size 1m; server { listen 80; server_name a.test; client_max_body_size 50m; location /up/ { client_max_body_size 2g; } } }`)
	has(t, res, "max_request_body 1mib", "max_request_body 50mib", "max_request_body 2gib")

	res = convert(t, serverConf(`client_max_body_size 0;`))
	wantIssue(t, res, "client_max_body_size", Approximated, "no limit")

	res = convert(t, serverConf(`client_max_body_size lots;`))
	wantIssue(t, res, "client_max_body_size", Unsupported, "not a size")

	res = convert(t, `http { client_header_timeout 20s; keepalive_timeout 75s; server { listen 80; server_name a.test; } }`)
	has(t, res, "header_timeout 20s", "idle_timeout 1m15s")

	res = convert(t, serverConf(`keepalive_timeout 5s;`))
	wantIssue(t, res, "keepalive_timeout", Approximated, "server-wide")
}

func TestRealIP(t *testing.T) {
	res := convert(t, `http { set_real_ip_from 10.0.0.0/8; set_real_ip_from 192.168.1.1; set_real_ip_from 2001:db8::/32; real_ip_header X-Forwarded-For; server { listen 80; server_name a.test; } }`)
	has(t, res, `trusted_proxies [ "10.0.0.0/8", "192.168.1.1/32", "2001:db8::/32" ]`)
	noIssue(t, res, "real_ip_header")

	res = convert(t, `http { set_real_ip_from 10.0.0.0/8; real_ip_header CF-Connecting-IP; server { listen 80; server_name a.test; } }`)
	wantIssue(t, res, "real_ip_header", Approximated, "X-Forwarded-For")

	res = convert(t, `http { set_real_ip_from nonsense; server { listen 80; server_name a.test; } }`)
	wantIssue(t, res, "set_real_ip_from", Unsupported, "not an address")
}

func TestNestedLocations(t *testing.T) {
	res := convert(t, serverConf(`location /app/ { location ~ \.json$ { return 200 "{}"; } location /app/static/ { try_files $uri =404; } }`))
	has(t, res, `route "/app/"`, `route ~ '\.json$'`, `route "/app/static/"`)
	noIssue(t, res, "location")

	res = convert(t, serverConf(`location ~ \.php$ { location /x/ { return 200 "x"; } }`))
	wantIssue(t, res, "location", Unsupported, "prefix location")
}

func TestLocationModifiers(t *testing.T) {
	res := convert(t, serverConf(`
        location = / { return 200 "root"; }
        location / { return 200 "any"; }
        location ^~ /static/ { return 200 "static"; }
        location ~ \.png$ { return 200 "png"; }
        location ~* \.JPG$ { return 200 "jpg"; }
        location @err { return 500; }`))
	has(t, res, `route = "/"`, `route "/"`, `route ^~ "/static/"`, `route ~ '\.png$'`, `route ~* '\.JPG$'`, `route "@err"`)

	res = convert(t, serverConf(`location ~ ^/(a|b)\1$ { return 200 "x"; }`))
	wantIssue(t, res, "location", Unsupported, "RE2")

	res = convert(t, serverConf(`location /a%20b/ { return 200 "x"; } location /q?x { return 200 "x"; } location relative { return 200 "x"; }`))
	if n := len(filterIssues(res, "location", Unsupported)); n != 2 {
		t.Errorf("two locations can't be written: %+v", res.Issues)
	}

	res = convert(t, serverConf(`location @Bad_Name { return 200 "x"; } location @bad.name { return 200 "x"; }`))
	has(t, res, `route "@bad_name"`)
	wantIssue(t, res, "location", Unsupported, "can't be used")
}

func filterIssues(res *Result, directive string, st Status) []Issue {
	var out []Issue
	for _, i := range res.Issues {
		if i.Directive == directive && i.Status == st {
			out = append(out, i)
		}
	}
	return out
}

func TestWhatCanNotBeTranslatedStaysVisible(t *testing.T) {
	res := convert(t, `
stream { server { listen 53; proxy_pass dns; } }
load_module modules/ngx_http_geoip_module.so;
http {
    map $uri $x { default 1; }
    server {
        listen 80; server_name a.test; root /srv;
        auth_basic "Private";
        location /files/ { alias /data/files/; autoindex on; expires 7d; }
        location /secret/ { internal; }
        location /x/ { set $a 1; charset utf-8; ssi on; some_third_party_thing on; }
        location /h/ { add_header X-A 1; }
        location /v/ { root /elsewhere; }
    }
}`)
	for _, d := range []string{"stream", "load_module", "map", "auth_basic", "alias", "autoindex", "expires", "internal", "set", "charset", "ssi", "some_third_party_thing"} {
		if issue(res, d, Unsupported, "") == nil {
			t.Errorf("%s should be reported as unsupported: %+v", d, res.Issues)
		}
	}
	wantIssue(t, res, "root", Unsupported, "document root of its own")
	// Unsupported directives are left in the output as comments, where they were.
	has(t, res, "# nginx line", "alias /data/files/;", "expires 7d;")
	if !res.NeedsReview() {
		t.Error("a configuration with unsupported directives needs review")
	}
}

func TestAddHeader(t *testing.T) {
	res := convert(t, serverConf(`add_header X-A "1"; add_header X-A "2"; add_header X-B $host; add_header "Bad Name" 1;`))
	has(t, res, `"X-A" = "1"`)
	lacks(t, res, `return 200 "2"`)
	wantIssue(t, res, "add_header", Unsupported, "already added")
	wantIssue(t, res, "add_header", Unsupported, "variables")
	wantIssue(t, res, "add_header", Unsupported, "not a valid header name")
}

func TestIncludes(t *testing.T) {
	res := convert(t, `http { include conf.d/*.conf; include missing/*.conf; include snippets/common.conf; }`, map[string]string{
		"conf.d/b.conf":          `server { listen 8082; server_name b.test; root /srv/b; }`,
		"conf.d/a.conf":          `server { listen 8081; server_name a.test; root /srv/a; include ../snippets/common.conf; }`,
		"snippets/common.conf":   `add_header X-Common yes;`,
		"conf.d/ignored.nonconf": `server { listen 9; }`,
	})
	if strings.Index(res.Config, `site "a.test"`) > strings.Index(res.Config, `site "b.test"`) {
		t.Errorf("includes are read in sorted order:\n%s", res.Config)
	}
	has(t, res, `"X-Common" = "yes"`, `root "/srv/a"`, `root "/srv/b"`)
	lacks(t, res, ":9\"")
}

func TestIncludeProblems(t *testing.T) {
	res := convert(t, `include missing.conf; http { server { listen 80; server_name a.test; } }`)
	wantIssue(t, res, "include", Unsupported, "could not read")

	// An include loop stops with an error instead of running forever.
	nodes, err := Parse(write(t, map[string]string{"nginx.conf": "include loop.conf;", "loop.conf": "include nginx.conf;"}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	res = Convert(nodes, false)
	wantIssue(t, res, "include", Unsupported, "could not read")
}

func TestFragmentWithoutHTTP(t *testing.T) {
	// A conf.d file holds server blocks without an http block around them.
	res := convert(t, `
upstream app { server 127.0.0.1:9000; }
server { listen 8080; server_name frag.test; location / { proxy_pass http://app; } }`)
	has(t, res, `site "frag.test"`, `upstream "app"`)
}

func TestAllListsExactTranslations(t *testing.T) {
	nodes, err := Parse(write(t, map[string]string{"nginx.conf": serverConf(`index a.html;`)}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(Convert(nodes, false).Issues); n != 0 {
		t.Errorf("a clean translation has no issues, got %d", n)
	}
	if res := Convert(nodes, true); issue(res, "index", Translated, "") == nil {
		t.Errorf("with all, exact translations are listed: %+v", res.Issues)
	}
}

func TestOutputIsDeterministic(t *testing.T) {
	a := convert(t, sample)
	for i := 0; i < 5; i++ {
		if b := convert(t, sample); a.Config != b.Config {
			t.Fatal("the same input produced different output")
		}
	}
}

func TestSurvivorsOfMutationTesting(t *testing.T) {
	if _, n, _ := size("1g"); n != 1<<30 {
		t.Errorf("1g = %d", n)
	}
	// TLS 1 alone still means a floor of 1.2.
	res := convert(t, `http { server { listen 443 ssl; server_name a.test; ssl_certificate /c.pem; ssl_certificate_key /k.pem; ssl_protocols TLSv1; } }`)
	has(t, res, `min_version "1.2"`)

	// A redirect is merged only into an https server, only when it goes to https, and only with a 3xx status.
	for name, redirect := range map[string]string{
		"plain target":  `server { listen 80; server_name a.test; return 301 http://a.test$request_uri; }`,
		"not a 3xx":     `server { listen 80; server_name a.test; return 200 https://a.test/; }`,
		"no https twin": `server { listen 81; server_name a.test; return 301 https://a.test$request_uri; }`,
	} {
		other := `server { listen 443 ssl; server_name a.test; ssl_certificate /c.pem; ssl_certificate_key /k.pem; }`
		if name == "no https twin" {
			other = `server { listen 82; server_name a.test; }`
		}
		res := convert(t, "http { "+redirect+" "+other+" }")
		if strings.Contains(res.Config, `scheme "http"`) {
			t.Errorf("%s: must not be merged:\n%s", name, res.Config)
		}
		wantIssue(t, res, "server", Unsupported, "one site per name")
	}

	// Error pages: one per status, and only for errors.
	res = convert(t, serverConf(`error_page 404 /a.html; error_page 404 /b.html; error_page 399 /c.html;`))
	wantIssue(t, res, "error_page", Unsupported, "already has an error page")
	wantIssue(t, res, "error_page", Unsupported, "only statuses 400 to 599")
	lacks(t, res, `error_page 404 "/b.html"`, `error_page 399 "/c.html"`)

	// A name two sites share is dropped from the second.
	res = convert(t, `http { server { listen 80; server_name a.test shared.test; } server { listen 81; server_name b.test shared.test; } }`)
	wantIssue(t, res, "server", Approximated, `"shared.test"`)
	if strings.Count(res.Config, `"shared.test"`) != 1 {
		t.Errorf("shared.test belongs to one site:\n%s", res.Config)
	}

	// Nothing after a return runs.
	res = convert(t, serverConf(`location /a/ { return 410; try_files $uri =404; }`))
	lacks(t, res, "try_files")

	// A case-insensitive pattern is checked too.
	res = convert(t, serverConf(`location /a/ { if ($http_x ~* "(?!a)") { return 403; } }`))
	wantIssue(t, res, "if", Unsupported, "RE2")

	// Routes can't repeat, and a nested one stays inside its parent.
	res = convert(t, serverConf(`location /a/ { return 200 "1"; } location /a/ { return 200 "2"; }`))
	wantIssue(t, res, "location", Unsupported, "already defined")
	lacks(t, res, `return 200 "2"`)
	res = convert(t, serverConf(`location /a/ { location /b/ { return 200 "x"; } }`))
	wantIssue(t, res, "location", Unsupported, "must start with that of the location")

	// A server that turns on only gzip_static gets its own compression block.
	res = convert(t, `http { gzip on; server { listen 80; server_name a.test; gzip_static on; } }`)
	has(t, res, "precompressed true")
}
