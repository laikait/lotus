# Moving from Nginx: `lotus compat nginx convert`

**Status:** implemented in `internal/nginxconv` and `cmd/lotus/compat_nginx.go`.
**Spec reference:** v1.1 §4.3.3, §19 (Nginx compatibility layer: a translator, not a second engine).

```text
lotus compat nginx convert [--json] [--all] [-o file [--force]] <nginx.conf>
```

It reads an Nginx configuration (following `include`, with globs) and writes the same behavior in the LOTUS language. It never guesses silently. Every directive ends up in one of four states:

| State | Meaning | Shown by default | Affects the exit code |
|---|---|---|---|
| translated | LOTUS does the same thing | only with `--all` | no |
| **approximated** | LOTUS does the nearest thing, and it is not exactly what Nginx did | yes | yes (6) |
| **unsupported** | The behavior is lost: LOTUS has nothing like it | yes | yes (6) |
| ignored | Nothing to translate: LOTUS has no such setting and needs none (`sendfile`, `worker_processes`, `ssl_ciphers`, ...) | only with `--all` | no |

Every approximated and unsupported directive is also left in the output as a comment, at the place where it was:

```text
# nginx line 12, unsupported: alias maps a location to another directory; LOTUS serves one root per site
#     alias /data/files/;
route "/files/" {
}
```

so the result can be reviewed in one place, in the file you are going to edit anyway. Exit codes: 0 everything translated or ignored, 6 something needs review, 2 the Nginx file does not parse (or, which would be a bug, the translation is not a valid LOTUS file), 7 file not found.

The translation is always a file `lotus config validate` accepts. A test generates thousands of random Nginx configurations, odd values included, and checks that. Another runs a translated configuration in a real LOTUS server and compares its answers with what Nginx gives (location precedence, `try_files`, `rewrite`, `return`, `if`, `deny`, proxying, error pages, `gzip`, rate limits).

Without `-o` the configuration goes to standard output and the report to standard error, so `lotus compat nginx convert nginx.conf > site.conf` works. With `-o file` the report goes to standard output; an existing file is not overwritten without `--force`. `--json` prints one JSON document (the configuration, the counts and every issue).

After converting, run `lotus config validate site.conf`, then `lotus config explain site.conf http://example.com/some/url` for the URLs that matter: it shows which route, rewrite and file a request gets, without sending it.

## What is translated

| Nginx | LOTUS |
|---|---|
| `server`, `server_name` (`.example.com`, `*.example.com`, several names) | `site`, `aliases` |
| `listen` (address, port, `ssl`, `default_server`, `quic`) | `listen` blocks, `listeners`, `protocol https`, `http3 true`; the `default_server` site is written first |
| `ssl_certificate`, `ssl_certificate_key`, `ssl_protocols` | `tls manual`, `certificate`, `certificate_key`, `tls { min_version }` |
| `root`, `index`, `error_page` | `root`, `index`, `error_page` |
| `location` (`=`, `^~`, `~`, `~*`, `@name`, nested) | `route` (the same precedence: `docs/architecture/routing-precedence.md`) |
| `try_files`, `rewrite` (`last`, `redirect`, `permanent`), `return` | the same directives, with `$uri`, `$args`, `$1`... turned into `{uri}`, `{query}`, `{re.1}` |
| `if ($request_method = POST) { return 405; }` and the same shape on a header, query argument, cookie, scheme, host or client address | `match { ... }` |
| `allow ...; deny all;` and `deny ...; allow all;` | `match { client_ip !~ [ ... ]  return 403 }` (or `=`) |
| `upstream` (`server`, `weight`, `max_conns`, `max_fails`, `fail_timeout`, `least_conn`, `ip_hash`, `random`, `hash $request_uri`) | `upstream`, `backend`, `balance` |
| `proxy_pass`, `proxy_set_header`, `proxy_connect_timeout`, `proxy_read_timeout` | `proxy`, `proxy_set_header`, `proxy_host`, upstream timeouts |
| `fastcgi_pass` (address, socket or a one-server upstream), `fastcgi_read_timeout` | `php_backend`, `php` |
| `proxy_cache_path`, `proxy_cache`, `proxy_cache_valid`, `proxy_cache_use_stale`, `proxy_cache_lock_timeout` (and the `fastcgi_` forms) | `cache` zone and route `cache` |
| `gzip`, `gzip_types`, `gzip_min_length`, `gzip_comp_level`, `gzip_static` | `compression` (gzip only; see below) |
| `limit_req_zone`, `limit_req` | `rate_limit` |
| `add_header` (fixed values) | `security_headers` |
| `access_log`, `error_log` | `logs` |
| `client_max_body_size`, `keepalive_timeout`, `client_header_timeout` | `max_request_body`, `idle_timeout`, `header_timeout` |
| `set_real_ip_from` | `trusted_proxies` |

## Decisions the translator makes for you

- **A port 80 server that only redirects to https is merged into the https server.** LOTUS has one site per host name, and a name can't be defined twice, so the usual pair (`listen 80; return 301 https://$host$request_uri;` and `listen 443 ssl;` with the same names) becomes one site on both listeners. Every route of the site gets a first `match { scheme "http"  return 301 "https://{host}{uri}" }`, and a `route "/"` is added when there is none, so every path is covered. A catch-all redirect server (`server_name _`) stays a site of its own. The ACME `location /.well-known/acme-challenge/` of such a server is dropped: LOTUS answers HTTP-01 challenges itself.
- **Nginx sends the backend's own host name as `Host`** unless `proxy_set_header Host $host` is set; LOTUS keeps the client's by default. The translator writes `proxy_host upstream` when the `Host` header is not set, so the backend sees what it saw before.
- **`proxy_pass` with a URI** (`proxy_pass http://app/;` inside `location /api/`) replaces the matched part of the path in Nginx. LOTUS sends the path as it is, so this is reported as approximated unless the URI equals the location (`location / { proxy_pass http://app/; }` is the same thing in both).
- **IPv4 and IPv6 wildcards** (`listen 80;` and `listen [::]:80;`) become one dual-stack listener, `[::]:80`.
- **A server-level `return`** answers before any location is looked at in Nginx, so the locations of that server are not translated (ignored, with a note).
- **`gzip on` alone** compresses `text/html` above 20 bytes at the fastest level in Nginx, and the translation says exactly that (`types [ "text/html" ]`, `min_size 20b`, `level fast`). When `application/javascript` or `text/javascript` is listed, both are written, because Nginx's `mime.types` and LOTUS may name `.js` files differently.
- **`limit_req ... burst=N`** becomes `burst N+1`: Nginx's burst is the queue beyond the one allowed request. Without `nodelay`, Nginx delays the burst and answers 503 over the limit; LOTUS refuses at once with 429 (reported as approximated unless `nodelay` and `limit_req_status 429` are there).
- **A cache zone is memory in LOTUS.** `proxy_cache_path ... max_size=1g` becomes a 1 GiB memory budget (reported, because there is no disk cache yet). `proxy_cache_use_stale error timeout ...` becomes `stale_if_error 1h` (Nginx has no time limit).
- **A name that two servers share** (the same `server_name` on different ports) can't be kept twice: the first server wins, the second is reported and left out.
- **A server that listens with `ssl` and names no usable certificate** gets `certificate "/etc/lotus/certs/CHANGE-ME-fullchain.pem"` placeholders and an unsupported note, so it fails to start with a clear message instead of serving without TLS. (LOTUS can also get certificates itself: `tls { acme { ... } }` and `tls automatic`.)

## What it cannot translate

These are reported as unsupported, and left in the output as comments:

`alias` (and a `root` that differs per location), `expires`, `auth_basic` (LOTUS has Basic authentication in `.htaccess` only), `autoindex`, `set`, `map`, `geo`, `split_clients`, `charset`, `ssi`, `sub_filter`, `auth_request`, `mirror`, `limit_conn` (only `limits { max_connections_per_ip }`, server-wide, exists), `internal` on a path location, client certificates (`ssl_verify_client`), custom cache keys and `proxy_cache_bypass` / `proxy_no_cache`, `add_header` inside a location (headers are per site), `gzip` per location, `rewrite` and `if` outside a location, `if` with anything but one `return` and a simple test, `proxy_pass` or `fastcgi_pass` with variables, regular expressions that need PCRE (lookahead, backreferences: LOTUS uses RE2), `stream {}` and `mail {}` blocks, `load_module` and every third-party module directive (Lua, ModSecurity, Brotli, ...), and any directive the translator does not know.

Behavior that depends on where Nginx's own files live is not followed: `include mime.types`, `fastcgi_params` and the like are built into LOTUS and are ignored; a relative path in a directive such as `ssl_certificate` is written as it is, so give absolute paths (Nginx reads them relative to its prefix, LOTUS relative to the configuration file).

## Not built yet

Converting Apache `httpd.conf` (`lotus compat apache convert`) is not implemented. `lotus compat htaccess check` reports what a `.htaccess` file needs.
