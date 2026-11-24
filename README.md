# LOTUS — Laika Optimized Transport & Unified Server

**A next-generation, cross-platform web server.**

An independent, cross-platform web server written in Go. It's built for PHP and shared hosting, migration from Apache and Nginx, and control-panel integration.

> **Status: Phase 1 (foundation), in progress.** LOTUS can already serve static websites, but it is not ready for production.

## What works today

- **Configuration:** the `.lotus` language (parser, includes, variables, `env()`/`file()`), a directive registry, validation with line/column errors, and `lotus config validate` / `lotus config inspect` (secrets redacted) / `lotus config explain <file> <url>`, which shows the site, route, `match` block, rewrites, `try_files` candidates and error page a request would get, step by step, without sending it.
- **Serving:** `lotus start` runs HTTP/1.1 and HTTPS (HTTP/2 included), virtual hosts with wildcard names, SNI certificate selection (`tls manual`), static files (ETag, Range, HEAD, index files, no directory listings), security headers, access logs (Combined format), connection and body limits, and graceful shutdown.
- **Routing:** `route` blocks follow the Nginx `location` model (exact, prefix, `^~`, regex, named routes), with `return` (redirects and fixed responses), `try_files` (including `=404` and `@named` fallbacks, at most 10 internal redirects), `match` blocks (method, header, query, cookie, client IP, scheme, host), `rewrite` (`last`, `redirect`, `permanent`), `error_page` (a path or `@named` route, original status kept), per-route `root` and `max_request_body`, and request-time placeholders such as `{uri}` and `{re.1}`. See `configs/examples/routes.lotus`.
- **Client IP:** `trusted_proxies` makes LOTUS believe `Forwarded` / `X-Forwarded-For` only from the listed networks, reading the chain from the right and skipping trusted hops. Access logs, `client_ip` conditions and `{remote_ip}` use the resolved address.
- **Reload:** on Unix, `kill -HUP <pid>` re-reads the configuration and switches to it without dropping connections. The new configuration is built completely first (files, logs, certificates); if the file is invalid or anything fails, the old configuration keeps running and the reason is logged. Sites, routes, certificates, log files and `trusted_proxies` can change; listeners, connection limits, timeouts and the minimum TLS version need a restart, and the reload says so. Reloading also reopens log files, so it is the hook for log rotation. The same works on every platform through the control channel: `lotus reload`.
- **Control:** a running server listens on a local control socket (owner-only). `lotus status`, `lotus reload`, `lotus stop` and `lotus logs reopen` use it; it is the way to reload on Windows. It is on by default; set another path with `control { socket "..." }`. See `docs/architecture/control-protocol.md` for the protocol and what is not built yet (viewer roles, Windows ACL checks).
- **Hardening:** a request with a chunked body closes its connection after the response (the defence against request smuggling when a proxy in front frames the request differently). Requests that `net/http` rejects before routing (malformed, too large, too slow) are counted, shown in `lotus status`, and logged with the peer address, rate-limited so garbage can't flood the log.
- **PHP:** the `php` route action runs scripts through FastCGI (PHP-FPM or `php-cgi`) over TCP or a Unix socket, with connection pooling and timeouts. Only existing `.php` files below the document root run (a path like `/uploads/evil.jpg/x.php` is a 404), `php_deny` and the site-wide guard keep `.php` source from ever being served as text, and the CGI environment drops `Proxy` (httpoxy) and underscore headers. Where there is no PHP-FPM (Windows especially), `php_backend { command "php-cgi" processes 4 }` makes LOTUS start and supervise the php-cgi processes itself: they are restarted when they die, recycled after `max_requests`, kept across reloads, given a minimal environment, and stopped with the server (on Windows even if the server is killed). See `docs/architecture/php.md`, `configs/examples/php.lotus` and `configs/examples/php-managed.lotus`.
- **`.htaccess`:** with `htaccess true` a site reads Apache `.htaccess` files, for the directives PHP applications ship: `RewriteEngine`/`RewriteBase`/`RewriteCond`/`RewriteRule` (with Apache's per-directory semantics, so WordPress permalinks and Laravel's front controller work as they are), `Redirect`/`RedirectMatch`, `ErrorDocument`, `DirectoryIndex`, `Options -Indexes`, `Require` and `Order`/`Allow`/`Deny`, HTTP Basic authentication (`AuthType Basic` with an `.htpasswd` file: bcrypt, `$apr1$`, `{SHA}`), `Header`, `Expires*`, `AddDefaultCharset`, `AddType`, `php_value`/`php_flag` (PHP-FPM backends), inside `<IfModule>`, `<Files>` and `<FilesMatch>`. It is off by default, enabled and limited per site by the administrator, never executed as code, and cannot loosen the site's symlink, dotfile or security-header policy. A directive LOTUS can't apply answers 500 and is logged with its line (never silently skipped; access rules are never skipped even in `ignore` mode). `lotus compat htaccess check <file>` reports what a file needs before you enable it. See `docs/architecture/htaccess.md` and `configs/examples/htaccess.lotus`.
- **Reverse proxy:** `upstream` groups of `http://`, `https://` and `unix:` backends, and the `proxy` route action. Load balancing (`round_robin` with weights, `least_conn`, `random`, `ip_hash`, `uri_hash`), passive and active health checks, retries only where a request is safe to repeat, TLS to backends verified by default (own CA, client certificates), WebSocket and streaming responses, `X-Forwarded-*` that the client can't forge, and the site's security headers that a backend can't override. See `docs/architecture/proxy.md` and `configs/examples/proxy.lotus`.
- **Limits:** `max_connections_per_ip`, `rate_limit` (token bucket per client address, per site and per route, answering 429 with `Retry-After` before any file, script, password check or backend is touched), and a lock-out after wrong Basic passwords. Bounded in memory; IPv6 clients are counted per /64; trusted proxies aren't counted as one client. See `docs/architecture/limits.md` and `configs/examples/limits.lotus`.
- **Automatic HTTPS:** a site on an `https` listener gets its certificate from Let's Encrypt (or any ACME CA) with HTTP-01 or TLS-ALPN-01 (answered by LOTUS's own listeners) or DNS-01 (Cloudflare, or a program of yours; this is what wildcard certificates need), renewed in the background, reused across restarts. Only names of configured sites are ever requested (no on-demand issuance); tested against Pebble. See `docs/architecture/acme.md`, `configs/examples/acme.lotus` and `configs/examples/acme-wildcard.lotus`.
- **HTTP/3:** `http3 true` on an `https` listener also serves HTTP/3 over UDP on the same port number and announces it with `Alt-Svc`. Same sites, limits, proxy and logs as the other protocols; connection limits count QUIC connections; 0-RTT is refused. See `docs/architecture/http3.md` and `configs/examples/http3.lotus`.
- **Metrics:** an optional `metrics` listener serves Prometheus-format metrics: requests by site, protocol and status class, latency histograms, connections, proxy backend health, PHP process restarts, reloads and certificate expiry. Labels are only names from the configuration, never client input; a listener that isn't on loopback needs a token or an allow list. See `docs/architecture/metrics.md` and `configs/examples/metrics.lotus`.
- **Safety:** every URL-to-file mapping goes through one resolver that blocks traversal, hidden files, symlink escapes and (on Windows) device names.

**Not implemented yet.** A configuration that uses any of these is refused at startup with a clear message, never silently ignored: per-site timeouts, control `viewer_group`. PROXY protocol is not available either (no directive for it yet).

## Try it

```sh
go build -o bin/lotus ./cmd/lotus
bin/lotus config validate configs/examples/static-site.lotus
bin/lotus start -config configs/examples/static-site.lotus     # then open http://127.0.0.1:8080/
```

## Documents

| Document | Purpose |
|---|---|
| [Technical Specification v1.1](LOTUS-Technical-Specification-v1.1.md) | The authoritative product and architecture specification |
| [Spec review](docs/spec-review.md) | Review of v1.0 that led to v1.1 |
| [Configuration grammar](docs/configuration/grammar.md) | Syntax and meaning of the `.lotus` configuration language |
| [Routing precedence](docs/architecture/routing-precedence.md) | How a request is matched to a route |
| [`.htaccess` support](docs/architecture/htaccess.md) | Which directives are supported, how rewrite rules behave, what is refused |
| [Automatic certificates](docs/architecture/acme.md) | ACME, challenges, storage, what is requested and what never is |
| [Metrics](docs/architecture/metrics.md) | The Prometheus endpoint: what it exposes, who can read it, example alerts |
| [HTTP/3](docs/architecture/http3.md) | QUIC over UDP, Alt-Svc, what is the same and what differs, system settings |
| [Rate limits](docs/architecture/limits.md) | Connection limits per address, request rate limits, password lock-out |
| [Reverse proxy](docs/architecture/proxy.md) | Upstreams, balancing, health checks, retries, what is forwarded and what is not |
| [PHP through FastCGI](docs/architecture/php.md) | Which file runs, what PHP sees, how failures are reported |
| [Control protocol](docs/architecture/control-protocol.md) | How the CLI talks to a running server |
| [Directive template](docs/configuration/directive-template.md) | How every directive is documented |
| [Estimates](docs/roadmap/estimates.md) | Effort and calendar estimates up to 1.0 |
| [Governance](GOVERNANCE.md) · [Security](SECURITY.md) | Project rules, versioning, vulnerability reporting |

## Building

Requires Go (version in `go.mod`).

```sh
make build        # bin/lotus
make check        # gofmt, go vet, tests
make race lint vuln
make cross        # Linux, FreeBSD, Windows, macOS binaries
```

On Windows without `make`: `go build -o bin/lotus.exe ./cmd/lotus` and `go test ./...`.

Some tests only run on Linux (symlinks, file permissions) or skip on Windows. From a Windows machine with WSL, `scripts/test-linux-wsl.sh` (run in Git Bash) cross-compiles the tests and runs them on Linux.

## License

Apache License 2.0 — see [LICENSE](LICENSE).
