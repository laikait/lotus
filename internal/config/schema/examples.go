package schema

// examples holds one snippet per directive and context, keyed "context/name".
// Each is written as it would appear inside that context, and the tests in
// internal/docgen check that every one of them parses and validates (inside a
// minimal configuration that also defines an upstream "app", a php_backend
// "php", a cache zone "pages" and a listener "http").
var examples = map[string]string{
	// tls › acme
	"acme/ca":            `ca "https://acme-staging-v02.api.letsencrypt.org/directory"`,
	"acme/ca_root":       `ca_root "/etc/ssl/pebble-root.pem"`,
	"acme/challenges":    `challenges [ "tls-alpn-01" ]`,
	"acme/dns":           "dns {\n    provider cloudflare\n    api_token env(\"CLOUDFLARE_API_TOKEN\")\n}",
	"acme/eab_hmac_key":  `eab_hmac_key env("ACME_EAB_HMAC_KEY")`,
	"acme/eab_key_id":    `eab_key_id "kid-1"`,
	"acme/email":         `email "admin@example.com"`,
	"acme/http_port":     `http_port 8080`,
	"acme/storage":       `storage "/var/lib/lotus/certs"`,
	"acme/tls_alpn_port": `tls_alpn_port 8443`,
	"dns/api_token": `provider cloudflare
api_token env("CLOUDFLARE_API_TOKEN")`,
	"dns/command": `provider exec
command "/usr/local/bin/acme-dns-hook"`,
	"dns/propagation_check": `provider cloudflare
api_token env("CLOUDFLARE_API_TOKEN")
propagation_check false`,
	"dns/propagation_delay": `provider cloudflare
api_token env("CLOUDFLARE_API_TOKEN")
propagation_delay 20s`,
	"dns/propagation_timeout": `provider cloudflare
api_token env("CLOUDFLARE_API_TOKEN")
propagation_timeout 5m`,
	"dns/provider": `provider cloudflare
api_token env("CLOUDFLARE_API_TOKEN")`,
	"dns/resolvers": `provider cloudflare
api_token env("CLOUDFLARE_API_TOKEN")
resolvers [ "1.1.1.1:53", "8.8.8.8:53" ]`,

	// upstream
	"upstream/backend":                 "backend \"http://10.0.0.5:8080\" {\n    weight 3\n    max_conns 100\n}",
	"upstream/balance":                 "balance least_conn",
	"upstream/connect_timeout":         "connect_timeout 2s",
	"upstream/fail_timeout":            "fail_timeout 30s",
	"upstream/health_check":            "health_check {\n    path \"/healthz\"\n    interval 5s\n}",
	"upstream/idle_timeout":            "idle_timeout 30s",
	"upstream/max_conns":               "max_conns 500",
	"upstream/max_fails":               "max_fails 5",
	"upstream/response_timeout":        "response_timeout 30s",
	"upstream/retries":                 "retries 2",
	"upstream/tls_ca":                  `tls_ca "/etc/ssl/internal-ca.pem"`,
	"upstream/tls_client_cert":         "tls_client_cert \"/etc/lotus/client.pem\"\ntls_client_key \"/etc/lotus/client.key\"",
	"upstream/tls_client_key":          "tls_client_cert \"/etc/lotus/client.pem\"\ntls_client_key \"/etc/lotus/client.key\"",
	"upstream/tls_server_name":         `tls_server_name "api.internal"`,
	"upstream/tls_verify":              "tls_verify true",
	"backend/max_conns":                "max_conns 50",
	"backend/weight":                   "weight 5",
	"health_check/healthy_threshold":   "healthy_threshold 3",
	"health_check/interval":            "interval 5s",
	"health_check/path":                `path "/healthz"`,
	"health_check/timeout":             "timeout 2s",
	"health_check/unhealthy_threshold": "unhealthy_threshold 2",

	// cache, compression
	"cache/max_entry_size":       "max_entry_size 4mib",
	"cache/max_size":             "max_size 256mib",
	"compression/algorithms":     `algorithms [ "br", "gzip" ]`,
	"compression/enabled":        "enabled false",
	"compression/level":          "level fast",
	"compression/min_size":       "min_size 2kib",
	"compression/precompressed":  "precompressed true",
	"compression/types":          `types [ "text/*", "application/json" ]`,
	"route_cache/bypass_cookies": `bypass_cookies [ "session", "wordpress_logged_in_*" ]`,
	"route_cache/lock_timeout":   "lock_timeout 3s",
	"route_cache/negative_ttl":   "negative_ttl 30s",
	"route_cache/stale_if_error": "stale_if_error 1h",
	"route_cache/ttl":            "ttl 5m",

	// server-level settings
	"control/socket":                `socket "/run/lotus/control.sock"`,
	"control/viewer_group":          `viewer_group "lotus-viewers"`,
	"limits/header_timeout":         "header_timeout 10s",
	"limits/idle_timeout":           "idle_timeout 1m",
	"limits/max_connections":        "max_connections 20000",
	"limits/max_connections_per_ip": "max_connections_per_ip 50",
	"limits/max_header_bytes":       "max_header_bytes 32kib",
	"limits/max_request_body":       "max_request_body 25mib",
	"limits/read_timeout":           "read_timeout 30s",
	"limits/write_timeout":          "write_timeout 30s",
	"listen/address":                `address "0.0.0.0:8081"`,
	"listen/http3":                  "protocol https\nhttp3 true",
	"listen/protocol":               "protocol https",
	"metrics/address":               `address "127.0.0.1:9100"`,
	"metrics/allow":                 "address \"127.0.0.1:9100\"\nallow [ \"10.0.0.0/8\" ]",
	"metrics/bearer_token":          "address \"127.0.0.1:9100\"\nbearer_token env(\"METRICS_TOKEN\")",
	"metrics/path":                  "address \"127.0.0.1:9100\"\npath \"/internal/metrics\"",
	"php_backend/command":           "command \"/usr/bin/php-cgi\"\nprocesses 4",
	"php_backend/fastcgi_pass":      `fastcgi_pass "unix:/run/php/php-fpm.sock"`,
	"php_backend/max_requests":      "command \"/usr/bin/php-cgi\"\nmax_requests 1000",
	"php_backend/php_ini":           "command \"/usr/bin/php-cgi\"\nphp_ini \"/etc/php/php.ini\"",
	"php_backend/processes":         "command \"/usr/bin/php-cgi\"\nprocesses 8",
	"php_backend/timeout":           "fastcgi_pass \"127.0.0.1:9000\"\ntimeout 30s",
	"runtime/max_procs":             "max_procs 4",
	"runtime/memory_limit":          "memory_limit 2gib",
	"tls/acme":                      "acme {\n    email \"admin@example.com\"\n}",
	"tls/min_version":               `min_version "1.3"`,
	"root/server":                   "server {\n    listen \"http\" {\n        address \"0.0.0.0:80\"\n    }\n}",
	"server/cache":                  "cache \"images\" {\n    max_size 256mib\n}",
	"server/compression":            "compression {\n    algorithms [ \"br\", \"gzip\" ]\n}",
	"server/control":                "control {\n    socket \"/run/lotus/control.sock\"\n}",
	"server/limits":                 "limits {\n    max_connections 20000\n    max_request_body 25mib\n}",
	"server/listen":                 "listen \"https\" {\n    address \"0.0.0.0:443\"\n    protocol https\n}",
	"server/metrics":                "metrics {\n    address \"127.0.0.1:9100\"\n}",
	"server/php_backend":            "php_backend \"app\" {\n    fastcgi_pass \"unix:/run/php/php-fpm.sock\"\n}",
	"server/runtime":                "runtime {\n    max_procs auto\n    memory_limit 2gib\n}",
	"server/site":                   "site \"example.com\" {\n    root \"/var/www/example\"\n}",
	"server/tls":                    "tls {\n    min_version \"1.2\"\n}",
	"server/trusted_proxies":        `trusted_proxies [ "10.0.0.0/8", "192.168.0.0/16" ]`,
	"server/upstream":               "upstream \"api\" {\n    backend \"http://127.0.0.1:9001\"\n    backend \"http://127.0.0.1:9002\"\n}",

	// site
	"site/aliases":                `aliases [ "www.example.com", "*.cdn.example.com" ]`,
	"site/certificate":            "tls manual\ncertificate \"/etc/ssl/example.pem\"\ncertificate_key \"/etc/ssl/example.key\"",
	"site/certificate_key":        "tls manual\ncertificate \"/etc/ssl/example.pem\"\ncertificate_key \"/etc/ssl/example.key\"",
	"site/compression":            "compression {\n    enabled false\n}",
	"site/dotfiles":               "dotfiles deny",
	"site/error_page":             "error_page 404 \"/404.html\"\nerror_page 502 \"@down\"",
	"site/htaccess":               "htaccess true",
	"site/htaccess_allow":         "htaccess true\nhtaccess_allow [ \"rewrite\", \"redirect\" ]",
	"site/htaccess_auth_failures": "htaccess true\nhtaccess_auth_failures 5",
	"site/htaccess_auth_lockout":  "htaccess true\nhtaccess_auth_lockout 10m",
	"site/htaccess_recheck":       "htaccess true\nhtaccess_recheck 5s",
	"site/htaccess_unsupported":   "htaccess true\nhtaccess_unsupported ignore",
	"site/index":                  `index [ "index.php", "index.html" ]`,
	"site/limits":                 "limits {\n    max_request_body 50mib\n}",
	"site/listeners":              `listeners [ "http" ]`,
	"site/logs":                   "logs {\n    access \"/var/log/lotus/example.access.log\"\n}",
	"site/rate_limit":             "rate_limit {\n    requests 10\n    per 1s\n    burst 20\n}",
	"site/root":                   `root "/var/www/example"`,
	"site/route":                  "route \"/blog/\" {\n    try_files [ \"{uri}\", \"{uri}/\", \"=404\" ]\n}",
	"site/security_headers":       "security_headers {\n    \"X-Content-Type-Options\" = \"nosniff\"\n    \"X-Frame-Options\" = \"DENY\"\n}",
	"site/symlinks":               "symlinks deny",
	"site/tls":                    "tls off",
	"logs/access":                 `access "/var/log/lotus/example.access.log"`,
	"logs/error":                  `error "/var/log/lotus/example.error.log"`,
	"rate_limit/burst":            "requests 5\nper 1s\nburst 10",
	"rate_limit/key":              "requests 1000\nper 1s\nkey global",
	"rate_limit/per":              "requests 100\nper 1m",
	"rate_limit/requests":         "requests 10\nper 1s",

	// route
	"route/cache":            "proxy \"app\"\ncache \"pages\" {\n    ttl 5m\n}",
	"route/error_page":       `error_page 404 "/errors/404.html"`,
	"route/index":            `index [ "index.html" ]`,
	"route/match":            "match {\n    method [ \"POST\", \"PUT\" ]\n    return 405 \"read only\"\n}",
	"route/max_request_body": "max_request_body 512mib",
	"route/php":              `php "php"`,
	"route/php_deny":         "php_deny true",
	"route/proxy":            "proxy \"app\"\nproxy_set_header \"X-Env\" \"production\"",
	"route/proxy_host":       "proxy \"app\"\nproxy_host upstream",
	"route/proxy_set_header": "proxy \"app\"\nproxy_set_header \"X-Env\" \"production\"",
	"route/rate_limit":       "rate_limit {\n    requests 5\n    per 1m\n}",
	"route/return":           "return 301 \"https://{host}{uri}\"",
	"route/rewrite":          `rewrite '^/blog/(\d+)$' "/posts/{re.1}.html"`,
	"route/root":             `root "/var/www/other"`,
	"route/route":            "route ~ '\\.json$' {\n    return 200 \"{}\"\n}",
	"route/try_files":        `try_files [ "{uri}", "{uri}/", "/index.php?{query}" ]`,

	// match
	"match/client_ip": "client_ip !~ [ \"10.0.0.0/8\" ]\nreturn 403",
	"match/cookie":    "cookie \"session\" ~ '^[a-f0-9]+$'\nreturn 200 \"signed in\"",
	"match/header":    "header \"User-Agent\" ~* 'bot|crawl'\nreturn 403",
	"match/host":      "host [ \"admin.example.com\" ]\nreturn 403",
	"match/method":    "method [ \"POST\" ]\nreturn 405",
	"match/php":       "method [ \"GET\" ]\nphp \"php\"",
	"match/php_deny":  "method [ \"PUT\" ]\nphp_deny true",
	"match/proxy":     "method [ \"POST\" ]\nproxy \"app\"",
	"match/query":     "query \"debug\" = '1'\nreturn 404",
	"match/return":    "method [ \"DELETE\" ]\nreturn 405 \"read only\"",
	"match/scheme":    "scheme \"http\"\nreturn 301 \"https://{host}{uri}\"",
	"match/try_files": "method [ \"GET\" ]\ntry_files [ \"{uri}\", \"=404\" ]",
}

// ExampleFor returns the example for a directive in a context, or "".
// (Directives that are shared between contexts, such as return in route and
// match, have an example per context, so it isn't a field of Directive.)
func ExampleFor(ctx Context, name string) string { return examples[string(ctx)+"/"+name] }

// ExampleKeys returns the keys of all examples, as "context/name".
func ExampleKeys() []string {
	keys := make([]string, 0, len(examples))
	for k := range examples {
		keys = append(keys, k)
	}
	return keys
}
