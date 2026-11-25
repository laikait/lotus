# LOTUS — Laika Optimized Transport & Unified Server
### A next-generation, cross-platform web server
## Product Architecture & Detailed Technical Implementation Specification

**Product:** LOTUS — Laika Optimized Transport & Unified Server
**Tagline:** A next-generation, cross-platform web server
**Binary:** `lotus` · **Repository:** `github.com/laikait/lotus`
**Vendor:** Laika IT
**Primary Language:** Go (Golang)
**Project Type:** Independent, general-purpose web server
**Target Operating Systems:** Linux (Tier 1), FreeBSD and Windows (Tier 2), others best-effort
**Configuration:** Custom native configuration language
**Extension Architecture:** Built-in modules, Wasm middleware, out-of-process extensions
**Compatibility Goals:** Incremental Nginx, Apache and `.htaccess` compatibility
**Engineering Priority:** Correctness, security, stability, then measured performance
**Document Version:** 1.1 (revision of 1.0 — see `docs/spec-review.md` and §26)

> **Decisions marked "⚑ Decision"** were made in this revision and **confirmed by the product owner on 2026-11-23**: positioning (PHP/shared hosting), 1.0 scope (core server, Linux Tier 1), Windows php-cgi supervisor, `.htaccess` subset in Phase 3, Nginx routing precedence, Wasm + out-of-process extensions, certmagic for ACME, Apache-2.0 license. Changing any of them later requires a spec revision.

---

# 1. Product Vision

LOTUS (Laika Optimized Transport & Unified Server) is an independent, general-purpose, high-performance web server written in Go.

LOTUS can be installed, configured and run on its own. It does not need another web server.

It covers conventional web hosting, PHP application hosting, reverse proxying, static content delivery, TLS termination with automatic certificates, load balancing and modern HTTP protocols.

## 1.1 What "independent" means

LOTUS owns everything above the wire protocol:

- Network listener and connection management.
- Configuration language, parser, validator and formatter.
- Virtual host management and routing.
- TLS policy and certificate management.
- Reverse proxy, FastCGI, static file, cache, compression and security engines.
- Logging, metrics, runtime/process management and control plane.
- Extension system and CLI.

⚑ **Decision — protocol layer.** LOTUS does **not** write its own HTTP/1.x, HTTP/2, QUIC or TLS parsers. It uses:

| Protocol | Implementation |
|---|---|
| HTTP/1.0, HTTP/1.1 | Go `net/http` |
| HTTP/2 (TLS and h2c) | Go `net/http` (`http.Server.Protocols`) / `golang.org/x/net/http2` |
| HTTP/3 / QUIC | `quic-go` (maintained, pure Go) |
| TLS 1.2 / 1.3 | Go `crypto/tls` |

Consequences, accepted deliberately:

- Request parsing, header canonicalization and smuggling defenses behave the way `net/http` does. LOTUS inherits the Go team's security fixes.
- LOTUS has no access to the raw request line or the original header order. Features that need them are out of scope.
- LOTUS uses Go's limits and timeouts (`ReadHeaderTimeout`, `MaxHeaderBytes`, etc.) and adds its own on top.

LOTUS must never use Nginx or Apache as its request-processing engine, and must not copy their source code.

## 1.2 Positioning and Differentiation

LOTUS is an alternative to Nginx, Apache HTTP Server, Caddy and LiteSpeed. Caddy is already a modular Go server with automatic TLS, so LOTUS sets itself apart on:

1. **PHP hosting as a first-class workload.** Native FastCGI, per-site PHP-FPM pools, safe script resolution, and ready-made templates for WordPress, Laravel, Symfony, Drupal and Joomla.
2. **Migration from Apache and Nginx.** Import tooling and `.htaccess` support for the directives common in PHP applications.
3. **Shared-hosting operation.** Per-site limits, logs, PHP pool isolation and per-site policies, all designed for many tenants on one host.
4. **Control-panel integration.** A restricted, documented management interface (used by LPanel, never required by LOTUS).

When features compete for priority, these four differentiators win.

## 1.3 Core Principles

1. Independent implementation above the protocol layer.
2. Cross-platform architecture, with Linux first.
3. Secure defaults.
4. Modular internal architecture with small interfaces.
5. Extensible without modifying the core.
6. Configuration-driven operation; invalid configuration is never activated.
7. Backward compatibility for stable configuration directives.
8. Predictable runtime behavior and bounded resource use.
9. Claims (performance, compatibility) are made only with evidence.

## 1.4 Non-Goals

These are out of scope for 1.x unless a later revision of this spec adds them:

- Writing HTTP or TLS protocol parsers by hand.
- CGI and Server-Side Includes (SSI).
- Mail proxying (SMTP/IMAP/POP3).
- Generic TCP/UDP stream proxying (Nginx `stream` equivalent).
- Embedded scripting languages in the request path (Lua, JavaScript).
- Multi-node clustering, distributed configuration and a shared runtime state. Certificate storage may be shared (§4.7).
- Running PHP inside the LOTUS process.
- Full, behavior-identical Nginx or Apache emulation.

# 2. Supported Platforms

## 2.1 Platform Tiers

| Tier | Meaning |
|---|---|
| **Tier 1** | Full CI (unit, integration, race, security), packages, production support. |
| **Tier 2** | CI builds and unit/integration tests; documented feature limitations; production support for documented features. |
| **Tier 3** | Expected to compile; no support commitment. |

| Platform | Tier at 1.0 |
|---|---|
| Ubuntu LTS, Debian stable | Tier 1 |
| RHEL-compatible (Rocky, Alma), Fedora | Tier 1 |
| FreeBSD | Tier 2 |
| Windows Server | Tier 2 |
| Windows Desktop, macOS | Tier 3 (development and testing) |
| OpenBSD | Tier 3 |

Platform limitations must be documented per feature (see §2.3). A test may be skipped on a platform only because of an actual platform limitation, and the skip must say which one.

## 2.2 Platform Abstraction

Operating-system code lives only in `internal/platform/...`, separated with Go build constraints. Application packages depend on small interfaces (§11.8), not on OS packages.

Responsibilities:

- Service installation and management.
- Signal / service-control event handling.
- Privilege management.
- Inherited listener sockets (socket activation, binary upgrade).
- Filesystem permission and ownership checks.
- File locking.
- Local control channel transport (§9.1).
- Resource monitoring.

## 2.3 Known Platform Differences

| Feature | Linux | FreeBSD | Windows |
|---|---|---|---|
| Bind :80/:443 without full root | `CAP_NET_BIND_SERVICE`, systemd sockets | `mac_portacl` or privileged start + drop | Service account with URL/port rights |
| Binary upgrade with zero downtime | FD inheritance | FD inheritance | Not supported in 1.0 (restart with short drain) |
| `SO_REUSEPORT` | Yes | Yes (`SO_REUSEPORT_LB`) | No |
| Zero-copy file send | `sendfile` via `io.Copy` | `sendfile` via `io.Copy` | `TransmitFile` via `io.Copy` |
| PHP backend | PHP-FPM | PHP-FPM | `php-cgi.exe` pool managed by LOTUS (§4.5.4) |
| Control channel | Unix socket | Unix socket | Named pipe |
| Log reopen | `SIGUSR1` | `SIGUSR1` | Control command only |

# 3. High-Level Architecture

## 3.1 Architectural Layers

### Layer 1: Core Runtime
Application lifecycle, configuration loading, module initialization, runtime state publication, startup, shutdown, reload, error propagation.

### Layer 2: Network and Protocol
Listener management (TCP and UDP), PROXY protocol, connection limits, TLS termination, HTTP/1.x/2/3 via the libraries in §1.1, WebSocket upgrade, connection draining.

### Layer 3: Request Processing
Request validation, real client IP resolution, virtual host selection, route matching, middleware chain, handler dispatch, response transformation, error handling.

### Layer 4: Service Engines
Static files, reverse proxy, FastCGI, cache, compression, security, authentication, certificate manager, load balancer.

### Layer 5: Extension System
Module registry, Wasm middleware runtime, out-of-process extension host, capabilities, version compatibility.

### Layer 6: Management
CLI, local control channel, optional network administrative API, metrics endpoint, diagnostics.

## 3.2 Request Processing Pipeline

1. Accept connection (listener enforces connection limits).
2. Parse PROXY protocol header, if enabled on that listener.
3. TLS handshake, if required (SNI selects the certificate).
4. Protocol negotiation (ALPN; HTTP/3 over UDP).
5. Parse the HTTP request (protocol library).
6. Validate the request (§4.10.2).
7. Resolve the real client IP (§4.10.4).
8. Resolve the virtual host.
9. Apply per-site and per-IP limits.
10. Match the route.
11. Run the middleware chain (built-in, then Wasm).
12. Dispatch to the handler (static, FastCGI, proxy, redirect, return, extension).
13. Apply response transformations (headers, compression).
14. Write the response.
15. Record metrics and access log.

Stages 6, 11 and 13 are extension points. Extensions cannot change stages 1–5.

## 3.3 Runtime State Model

All configuration-derived state (vhost table, routes, upstream pools, TLS config, policies) is compiled into one immutable `Runtime` value. It is published through `atomic.Pointer[Runtime]`. Request goroutines load the pointer once per request and never take a global lock to read configuration.

# 4. Detailed Feature Specification

## 4.1 HTTP Engine

Support:

- HTTP/1.0, HTTP/1.1, HTTP/2 (TLS and optional h2c), HTTP/3.
- Keep-alive and connection reuse.
- Request and response streaming; chunked transfer encoding.
- `Expect: 100-continue` handling. The body is only read after limits and routing accept it.
- WebSocket upgrades (HTTP/1.1; RFC 8441 over HTTP/2 when the library supports it).
- Timeouts: header read, body read, idle, write, and per-route handler timeout.
- Header count and size limits; body size limits.
- Graceful connection draining.

Requirements:

- HTTP/2 is enabled by default on TLS listeners **from Phase 1**, with a configuration switch to turn it off.
- HTTP/3 needs a UDP listener paired with a TCP/TLS listener. LOTUS advertises it through `Alt-Svc` only when the UDP listener is healthy.
- QUIC deployments document the OS UDP buffer tuning they need. `lotus diagnose` warns when buffers are too small.
- Each protocol has its own integration tests.
- No hand-written cryptography.

## 4.2 Virtual Host Engine

Support:

- Multiple sites per IP address and multiple IP addresses.
- Aliases, wildcard hostnames (`*.example.com`), and one default site per listener.
- SNI-based certificate selection.
- Per-site: document root, index files, access/error logs, security policy, resource limits, PHP backend.

Host lookup order (documented, deterministic):

1. Exact hostname match.
2. Longest wildcard suffix match.
3. Listener default site.
4. Otherwise: close the connection on TLS listeners if no certificate matches, or answer `421 Misdirected Request` / `404` on plaintext listeners, depending on configuration.

Site-to-listener binding is explicit (§5.1). A site listens on all of the server's listeners unless it names specific ones.

The Host header is validated against the TLS SNI name. A mismatch returns `421` unless the site allows it.

## 4.3 Routing Engine

### 4.3.1 Matching conditions
Exact path, prefix path, regular expression (RE2, Go `regexp`), HTTP method, hostname, request headers, query parameters, client IP/CIDR, scheme.

### 4.3.2 Features
Route priority, named routes, route groups (nesting), internal redirects (`try_files`, named routes), external redirects, rewrite rules, URI normalization, query-string manipulation, custom error handling.

### 4.3.3 Matching precedence

⚑ **Decision.** Path precedence uses the Nginx `location` model, so translated configurations keep their behavior:

1. **Exact** (`route = "/path"`): if it matches, stop.
2. **Prefix** routes: remember the longest match. If that route is marked `final` (Nginx `^~`), stop.
3. **Regex** routes, **in configuration order**: the first match wins.
4. Otherwise, the longest prefix from step 2.

Non-path conditions (method, header, query, IP, scheme) filter routes **after** a route is chosen by path, inside nested `match` blocks, and never change path precedence.

Internal redirects are limited to 10 per request. Going over the limit returns `500` and logs an error.

`lotus config explain <site> <method> <url>` prints the route that would be chosen and why.

Validation rejects routes that can never match (e.g., a duplicate exact path), and flags regex routes that hide later regex routes.

### 4.3.4 URI normalization
Before routing: percent-decode unreserved characters, resolve `.` and `..` segments, merge repeated slashes (configurable), and reject encoded `/` (`%2F`) and NUL bytes in the path unless the site allows them. The normalized path is used for routing and filesystem resolution. The original URI is passed to upstreams unless a rewrite changes it.

## 4.4 Static File Engine

Features:

- MIME detection (built-in table, overridable per site).
- Index files; directory listing (off by default).
- ETag and Last-Modified; conditional requests; Range requests; HEAD.
- **Pre-compressed sidecar files**: serve `file.br` / `file.zst` / `file.gz` when present and accepted by the client, with correct `Content-Encoding` and `Vary`.
- Custom error pages.
- Symlink policy: `follow`, `owner_match` or `deny` (default `owner_match` on Unix; `deny` for reparse points on Windows).
- Hidden-file policy: dotfiles are denied by default, except `/.well-known/`.
- File size limits; access restrictions.

Performance:

- Files are streamed with `io.Copy` from `*os.File` into the connection, so Go uses `sendfile` / `TransmitFile`. Response writers must not be wrapped in a way that hides `io.ReaderFrom`, unless the response is being transformed (e.g., compressed).
- An open-file / metadata cache, bounded in entries and lifetime.
- Large files are never loaded fully into memory.

All path resolution goes through a single function that joins the root, cleans the path, checks symlinks and confirms the result stays inside the root. Every engine that maps URLs to files uses it (static, FastCGI script resolution, `.htaccess` discovery).

## 4.5 PHP and FastCGI Engine

### 4.5.1 FastCGI protocol
A native FastCGI client, implemented and tested independently of PHP:

- Unix domain socket and TCP transports.
- Request multiplexing is off (PHP-FPM doesn't support it). Kept-alive connection pools per upstream.
- Request body streaming; response header parsing; stdout/stderr separation (stderr goes to the site error log).
- Connect, send and read timeouts.
- Upstream failure handling with load balancing across several FastCGI servers.

### 4.5.2 PHP integration
- Several PHP versions and per-site pools.
- Configurable FastCGI parameters, with safe defaults (`SCRIPT_FILENAME`, `PATH_INFO`, `DOCUMENT_ROOT`, `HTTPS`, etc.).
- Upload size limits and execution timeouts.
- Ready-made site templates: WordPress, Laravel, Symfony, Drupal, Joomla, generic front-controller. Templates are part of the product and have tests (§14.4).

### 4.5.3 Security
- The script path must resolve to an **existing regular file inside the site root** that matches the configured PHP extensions. Otherwise: `404`, and nothing is sent to FastCGI.
- `PATH_INFO` is split before the file existence check (prevents the `/upload.jpg/x.php` class of attack).
- PHP is never executed for files in configured upload directories.
- Source files (`.php` and others) are never served as static files when PHP is enabled for that extension.
- Socket permissions are checked at startup; group/world-writable sockets cause a warning.

### 4.5.4 Windows PHP backend
⚑ **Decision.** On Windows, LOTUS supervises a bounded pool of `php-cgi.exe` FastCGI processes per site: spawn, health-check, restart after N requests (`PHP_FCGI_MAX_REQUESTS`), and stop on shutdown. This ships in Phase 3 as Tier 2. PHP-FPM itself stays an independent service on Unix.

## 4.6 Reverse Proxy Engine

Support:

- HTTP, HTTPS and Unix socket upstreams; WebSocket proxying.
- Request and response buffering (configurable, bounded, spilling to disk above a threshold); streaming responses.
- Request rewriting; request and response header modification.
- `X-Forwarded-*` / `Forwarded` generation, with incoming values removed unless the client is a trusted proxy (§4.10.4).
- Upstream TLS verification on by default; custom CA; client certificates for upstreams.
- Connection pooling with per-upstream limits.
- Connect, read and write timeouts.

Load balancing: round robin, weighted round robin, least connections, random (power of two choices), consistent hashing (by key expression).

Health: active checks (HTTP or TCP), passive checks (failure counting), thresholds, recovery intervals, automatic removal and recovery.

A slow or failing upstream must not block other requests. Each upstream has its own concurrency limit, and its requests time out separately.

Retries only happen for idempotent methods, or when no bytes were sent to the upstream.

## 4.7 TLS and Certificate Management

- TLS 1.2 and 1.3. TLS 1.0/1.1 are not supported.
- SNI, ALPN, configurable cipher suites (TLS 1.2), curve preferences, session tickets with rotating keys.
- User-provided certificates; several certificates per site (ECDSA + RSA).
- Certificate reload without restart.
- Client certificate authentication (§4.11).

### 4.7.1 Automatic certificates (ACME)

⚑ **Decision.** Build on **`certmagic`** (Apache-2.0) for issuance, renewal, storage and locking, instead of writing an ACME client. DNS-01 providers come from the `libdns` ecosystem it uses.

- Let's Encrypt and other ACME CAs (including EAB for commercial CAs).
- HTTP-01, TLS-ALPN-01 and DNS-01 challenges. DNS-01 providers in 1.0: Cloudflare, Route 53 and RFC 2136. More come through the extension system.
- Wildcard certificates (DNS-01 only).
- Renewal at about two-thirds of the lifetime (or as told by ARI, ACME Renewal Information), retry with backoff, and expiry monitoring through metrics and logs.
- **Certificates are never issued inside a TLS handshake for a name that isn't configured.** On-demand issuance is off by default. When enabled, it requires an allow callback and rate limits.
- Certificate storage: local filesystem by default, behind a storage interface with locking, so several instances can share storage without breaking CA rate limits.

### 4.7.2 OCSP
Let's Encrypt shut down its OCSP service in 2025. OCSP stapling is **optional**: it's used only when a certificate contains an OCSP responder URL, with a cached response that is refreshed in the background. It is not required for 1.0.

## 4.8 Caching Engine

### 4.8.1 Response / proxy cache
- Cache keys from configurable expressions.
- `Cache-Control`, `Expires`, `Vary` and `Age` follow RFC 9111. Responses with `Set-Cookie`, `Authorization` or `private` are not cached unless explicitly permitted.
- Conditional revalidation (ETag / Last-Modified).
- Request coalescing (cache lock) to prevent stampedes.
- `stale-while-revalidate`, `stale-if-error`.
- Bypass rules, purge by key / prefix / tag, size limits, eviction (LRU or similar), statistics.

### 4.8.2 Storage
- In-memory: capped in **bytes**. Bodies are stored in byte slabs with no pointers, so the GC doesn't have to scan them.
- Filesystem: atomic write (temp file + rename), capped size, eviction in the background.
- Cache entries are streamed: a hit is served as a body reader, never a fully buffered value (§11.6).

## 4.9 Compression Engine

- Gzip (`compress/gzip` or `klauspost/compress`), Zstandard (`klauspost/compress/zstd`), Brotli (`andybalholm/brotli`).
- Configurable levels, MIME allowlist, minimum size, `Accept-Encoding` negotiation with q-values, correct `Vary: Accept-Encoding`.
- Already-compressed content (images, archives, `Content-Encoding` set) is skipped.
- Range requests on dynamically compressed responses are answered without compression (or the Range is ignored, as configured). They are never answered with a wrong byte range.
- CPU controls: dynamic Brotli is limited to level ≤ 5 by default, and a global limit caps concurrent compressions. Pre-compressed sidecars (§4.4) are the recommended path for static assets.

## 4.10 Security Engine

### 4.10.1 Network security
- IPv4/IPv6 allowlists and denylists (CIDR).
- Listener-level connection limits (global and per IP).
- Per-IP and per-key rate limiting (token bucket), in memory, bounded, with expiry of idle buckets.
- Header/body size limits and timeouts (§4.1).

### 4.10.2 HTTP security
- Host header validation; rejection of invalid header names and values.
- Request smuggling defenses: rely on `net/http`. In addition, reject `Transfer-Encoding` + `Content-Length` together and duplicate `Content-Length`, before proxying or FastCGI.
- Path normalization and traversal prevention (§4.3.4, §4.4).
- Method restrictions per route.
- Security header presets (HSTS, `X-Content-Type-Options`, `Referrer-Policy`, `Content-Security-Policy` as configured).

### 4.10.3 Access control
Basic auth, client certificates, IP rules, and route/directory-level restrictions. These are evaluated in a documented order (§4.11).

### 4.10.4 Trusted proxies and real client IP
- `trusted_proxies` CIDR list per server or site.
- The client IP comes from `Forwarded` / `X-Forwarded-For` (rightmost untrusted address) **only** when the direct peer is trusted.
- PROXY protocol v1/v2 per listener, accepted only from trusted sources.
- Every IP-based feature uses the resolved client IP: rate limits, allowlists, logs, consistent hashing.

### 4.10.5 Additional
- Optional WAF integration through the extension system (e.g., Coraza as a Wasm or built-in module). Not required for 1.0.
- Security audit log (auth failures, policy denials, admin actions).
- Configurable security policies per site.

Security controls are tested against known attack classes (§13.2), not just with functional tests.

## 4.11 Authentication Engine

- HTTP Basic auth with password files using bcrypt or argon2id. No proprietary hashing. Plaintext and MD5/crypt hashes are rejected.
- Client certificate authentication with configurable CA and optional subject/SAN matching.
- An authentication middleware interface for providers (including extensions).
- Order of evaluation: IP rules, then authentication, then authorization. Each route says whether **all** or **any** of them must pass.

## 4.12 Logging and Observability

### 4.12.1 Access logs
Common, Combined, custom templates and JSON. Per-site files, stdout/stderr, or syslog (Unix) / Event Log (Windows, error log only). Every request gets an ID (`X-Request-ID`, passed through when it comes from a trusted proxy).

Logs are written through a bounded buffer. When the buffer is full, the configured policy decides: block or drop and count. Never grow without limit.

Rotation: LOTUS reopens log files on `SIGUSR1` (Unix) or on the `lotus logs reopen` control command (all platforms), so external rotation tools work. Built-in size-based rotation is optional.

### 4.12.2 Error logs
Severity levels, structured fields (component, site, request ID), debug mode. Secrets are redacted (§5.2).

### 4.12.3 Metrics
Prometheus-compatible endpoint with: requests by site/status class, active connections, latency histograms, upstream latency and health, cache hits/misses, TLS handshake failures, certificate expiry, rate-limit rejections, memory, goroutines, and listener statistics.

Label sets are bounded: per site and per upstream, **never** per URL or client IP.

The metrics endpoint is off by default and, when on, restricted by IP rules and optional authentication.

## 4.13 Runtime Management

Start, stop, restart, graceful shutdown, graceful reload, binary upgrade (where supported), validation, status, health, diagnostics, log reopen, PID file (Unix).

### 4.13.1 Graceful reload (configuration)
1. Read and parse the new configuration.
2. Validate it fully, including file and certificate checks.
3. Compile a new `Runtime` (§3.3).
4. Open listeners that are new; reuse listeners whose address and protocol haven't changed.
5. Start new upstream pools and modules.
6. Publish atomically (`atomic.Pointer.Store`).
7. Requests already running finish with the old `Runtime`. Removed listeners stop accepting and drain.
8. Release old resources when their reference count reaches zero, or after the drain timeout.

If any of steps 1–5 fail, LOTUS keeps running the old configuration, reports the error through the control channel and logs it. There is never a period with no service.

### 4.13.2 Binary upgrade
On Linux and FreeBSD: the old process passes its listener file descriptors to the new binary, waits until the new process reports ready, then drains and exits. On Windows in 1.0: service restart with a configurable drain period. Brief unavailability is documented.

### 4.13.3 Shutdown
On `SIGTERM` / service stop: stop accepting, close idle connections, wait up to `shutdown_timeout` for running requests, then close everything.

# 5. Native Configuration Language

## 5.1 Illustrative configuration (revised)

```text
lotus_config_version 1

let $webroot = "/var/www"

server {
    runtime {
        max_procs auto          # GOMAXPROCS; "auto" = cgroup-aware CPU count
        memory_limit 2gib       # GOMEMLIMIT
    }

    listen "http" {
        address "0.0.0.0:80"
        protocol http
    }

    listen "https" {
        address "0.0.0.0:443"
        protocol https
        http3 true              # also binds UDP 443 and advertises Alt-Svc
    }

    trusted_proxies [ "10.0.0.0/8" ]

    limits {
        max_connections 10000
        max_request_body 100mib
        header_timeout 15s
    }

    tls {
        min_version "1.2"
        acme {
            email env("LOTUS_ACME_EMAIL")
        }
    }

    site "example.com" {
        listeners [ "http", "https" ]
        aliases [ "www.example.com" ]
        tls automatic

        root "${webroot}/example/public"
        index [ "index.php", "index.html" ]

        route "/" {
            try_files [ "{uri}", "{uri}/", "/index.php?{query}" ]
        }

        route ~ '\.php$' {      # single quotes = raw string (no escapes)
            php "php83"
        }

        logs {
            access "/var/log/lotus/example.access.log"
            error  "/var/log/lotus/example.error.log"
        }
    }

    php_backend "php83" {
        fastcgi_pass "unix:/run/php/php8.3-fpm-example.sock"
        timeout 60s
    }
}
```

## 5.2 Language requirements

- **`lotus_config_version`** is required in the root file. The parser supports the current version and the one before it. `lotus config migrate` upgrades files.
- Nested blocks, comments (`#`), typed values, arrays, maps.
- **Two kinds of substitution, written differently so they can't be confused:**
  - Load-time: `let $name = ...`, used as `${name}`, and `env("NAME")`. Resolved once when the config loads.
  - Request-time placeholders: `{uri}`, `{query}`, `{host}`, `{remote_ip}`, `{header.X-Name}`, etc. Only allowed in directives documented as accepting them. Using one anywhere else is a validation error.
- Types: string, integer, boolean, duration (`ms`, `s`, `m`, `h`, `d`), size (`kb`/`mb`/`gb` = powers of 10; `kib`/`mib`/`gib` = powers of 2), CIDR, regex (RE2, compiled during validation), list, map.
- `include "path/*.conf"`: resolved relative to the including file, sorted by name, cycle detection, depth limit 16. Files must not be world-writable (Unix).
- Merging: included blocks join the context they're included into. Defining the same singular directive twice in one context is an error, never "last one wins".
- Unknown directives and directives in the wrong context are errors, with file:line:column and a "did you mean" suggestion.
- Secrets (`env()`, values of directives marked `secret`) are never printed by `config inspect`, logs or diagnostics.
- Runtime changes (e.g., through the admin API) never overwrite the source configuration files.

⚑ **Decision.** The grammar is written as an EBNF document (`docs/configuration/grammar.md`, drafted in Phase 0) **before** the parser is implemented, and gets a fuzz corpus from the start. Where this section and the grammar document differ, the grammar document wins.

## 5.3 Configuration tooling

- `lotus config validate [--json]`
- `lotus config format [--check]`: stable formatting that doesn't change meaning; comments are preserved.
- `lotus config inspect [--resolved]`: the effective configuration, secrets redacted.
- `lotus config test`: validate and also check files, sockets and certificates exist.
- `lotus config explain <site> <method> <url>`: shows vhost, route and handler resolution.
- `lotus config migrate`: upgrades `lotus_config_version`.

# 6. Nginx and Apache Compatibility

Compatibility is built as **translators into native configuration**. There is no second runtime: imported configuration runs on the same engine as native configuration.

## 6.1 Compatibility levels

1. Syntax parsing.
2. Validation (directive and context).
3. Semantic translation.
4. Runtime behavior equivalence (verified by differential tests).
5. Application compatibility (verified by application test suites).

Each supported directive is documented with the level it reaches.

## 6.2 Nginx

Lexer/parser, directive registry, context validation, translation of: `server`, `listen`, `server_name`, `root`, `index`, `location` (all modifiers; precedence maps 1:1, §4.3.3), `try_files`, `rewrite`, `return`, `if` (limited safe subset), `proxy_pass`, `proxy_set_header`, `upstream`, `fastcgi_pass`, `fastcgi_param`, `client_max_body_size`, `access_log`, `error_log`, `add_header`, `ssl_certificate*`.

## 6.3 Apache

Parser and translation of: `Listen`, `VirtualHost`, `ServerName`, `ServerAlias`, `DocumentRoot`, `Directory`, `DirectoryIndex`, `Alias`, `Redirect`, `RewriteEngine`, `RewriteRule`, `RewriteCond`, `ProxyPass`, `ProxyPassReverse`, `SetHandler` (FastCGI only), `ErrorDocument`, `CustomLog`, `ErrorLog`, `Options`, `Require`, `Header`, `AuthType Basic` / `AuthUserFile`.

## 6.4 `.htaccess`

⚑ **Decision — moved earlier.** The WordPress/Laravel `.htaccess` subset (`RewriteEngine`, `RewriteCond`, `RewriteRule`, `Options -Indexes`, `DirectoryIndex`, `ErrorDocument`, `Require`, basic auth) ships in **Phase 3** together with PHP, because it supports differentiators 1 and 2 (§1.2). The full Apache translator stays in Phase 9.

Rules:

- Off by default; enabled per site.
- A per-site allowed-directive list. Directives not on it cause `500` + a log entry (configurable: or ignore + warn). Security-sensitive directives are never ignored silently.
- Discovery walks only from the site root down to the target directory, and never above the root.
- Parsed files are cached, keyed by path + mtime + size. Stat checks are throttled (configurable, default 2 s), so a request doesn't stat every directory every time.
- `.htaccess` is never executed as code and can't change anything outside its directory subtree.

## 6.5 Compatibility CLI

```bash
lotus compat nginx check  /path/to/nginx.conf  [--json]
lotus compat nginx import /path/to/nginx.conf  -o site.conf
lotus compat apache check  /path/to/httpd.conf
lotus compat apache import /path/to/httpd.conf -o site.conf
lotus compat htaccess check /var/www/example/.htaccess
```

Import produces native configuration and a report. Every unsupported or partly translated directive is listed with file:line. Import fails, rather than producing output, if any unsupported directive is security-sensitive (access control, auth, TLS, `deny`).

Complete Nginx or Apache compatibility is never claimed without conformance evidence.

# 7. Extension Architecture

## 7.1 Three tiers

| Tier | Mechanism | Use | Trust |
|---|---|---|---|
| Built-in modules | Compiled in; registered at init | Core engines | Fully trusted |
| **Wasm middleware** | `wazero` (pure Go, no cgo), in-process sandbox | Per-request middleware, request/response filters, custom auth | Untrusted; limited by capabilities |
| Out-of-process extensions | Separate process, versioned gRPC over a local socket | Custom handlers, async hooks (log sinks, cert DNS providers, WAF engines), long-running services | Untrusted; OS process isolation |

⚑ **Decision.** Middleware that runs on every request uses **Wasm**, not RPC, so it doesn't add a cross-process round-trip to every request. Out-of-process extensions are for handlers and asynchronous hooks. Go's `plugin` package is not used.

Optional: a custom-build tool (like `xcaddy`) for trusted, compiled-in third-party modules. This is a 1.x item.

## 7.2 Capabilities

Extensions get nothing by default. Capabilities that can be granted: read request headers / body, modify response, outbound network to listed hosts, read-only filesystem paths, emit metrics, read their own configuration. Wasm modules also get memory and fuel (CPU) limits per call, and a timeout. If an extension fails, the request fails closed or open, as configured per extension.

## 7.3 SDK

Public SDK under `pkg/sdk`: interfaces, Wasm guest SDK (Go via TinyGo, and Rust), gRPC protocol definitions, example extensions, a test harness, and semantic versioning of the extension API. Extensions are signed, and signatures are verified before loading when `require_signed_extensions` is on (default on in production profiles).

# 8. Command-Line Interface

Executable: `lotus`. Every command supports `--json`. Exit codes are documented (0 ok, 1 usage, 2 validation failure, 3 runtime unreachable, 4 permission denied, ...).

```bash
lotus version | help
lotus start | stop | restart | reload | upgrade | status
lotus config validate | format | inspect | test | explain | migrate
lotus site list | validate | inspect
lotus cert list | renew | inspect
lotus cache status | purge
lotus logs reopen
lotus metrics | health
lotus module list | inspect
lotus compat nginx|apache|htaccess check|import
lotus service install | remove          # platform service integration
lotus diagnose                          # environment checks (permissions, limits, UDP buffers, ports)
```

Commands that destroy data (`cache purge --all`, `service remove`) ask for confirmation unless `--yes` is given. Runtime commands talk to the running instance through the control channel (§9.1).

# 9. Control Plane and Administrative API

## 9.1 Local control channel (required)

- Transport: Unix domain socket (`/run/lotus/control.sock`, mode 0660, group `lotus-admin`), or a Windows named pipe (`\\.\pipe\lotus-control`) with an ACL limited to Administrators and the LOTUS service group.
- Protocol: versioned JSON-RPC.
- Operations: reload, status, upgrade, logs reopen, cache purge, cert renew, site/module inspection.
- The CLI always uses this channel. Signals (`SIGHUP` reload, `SIGTERM` stop, `SIGUSR1` log reopen, `SIGUSR2` binary upgrade) are also supported on Unix for compatibility with service managers.

## 9.2 Network administrative API (optional)

Disabled by default. When enabled: TLS required, token or mTLS authentication, role-based authorization (read-only vs operator), IP restrictions, request limits, audit log, versioned paths (`/v1/...`).

It exposes the same operations as the control channel. It never exposes arbitrary filesystem or OS operations, and never writes source configuration files.

## 9.3 LPanel integration

LOTUS is a separate product. LPanel/LPA deploy validated configuration files and call `reload` through the control channel or the admin API. LOTUS never depends on LPanel or LPA to start or serve sites.

# 10. Process and Privilege Model

## 10.1 Process
LOTUS is one process with many goroutines. "Workers" means goroutine scheduling capacity, set with `runtime.max_procs` (GOMAXPROCS, cgroup-aware). It does not mean separate processes.

## 10.2 Privileges
⚑ **Decision.** LOTUS never runs request handling as root / Administrator.

- **Linux (preferred):** start as the `lotus` user with `AmbientCapabilities=CAP_NET_BIND_SERVICE`, or use systemd socket activation.
- **Linux/FreeBSD (fallback):** start as root, bind listeners, read private keys, then drop to the `lotus` user before serving. Go 1.16+ applies `setuid`/`setgid` to all threads on Linux. Supplementary groups are cleared.
- **Windows:** run as a dedicated virtual service account (`NT SERVICE\lotus`) with only the rights it needs.
- Private keys stay readable only by root/the service account. After dropping privileges, they are only held in memory.

## 10.3 Multi-tenant isolation (explicit limits)

- PHP code runs in per-site PHP-FPM pools with per-site OS users. That is where process and filesystem isolation for dynamic code comes from.
- **Static files are read by the LOTUS process user for every site.** Virtual hosts alone do not isolate sites from each other. Operators must use filesystem permissions (e.g., site files readable by group `lotus`, not world-readable) and the symlink policy `owner_match` to stop one tenant's symlinks from reaching another tenant's files.
- Per-site limits: connections, request rate, body size, bandwidth (1.x), cache share.
- Per-site configuration boundaries: a site's included files cannot define other sites (validation rule), so tenant-managed include files can be allowed safely.

# 11. Core Interfaces (revised)

These are design contracts. Keep interfaces small.

## 11.1 Module

```go
type Module interface {
    Name() string
    Version() string
    Provision(ctx context.Context, cfg ModuleConfig, host Host) error
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
}

// Host offers only narrow services, never a general-purpose dependency container.
type Host interface {
    Logger(component string) *slog.Logger
    Metrics() MetricsRegistry
}
```

## 11.2 Handler and Middleware
Use `http.Handler` and `func(http.Handler) http.Handler`.

## 11.3 Configuration

```go
type ConfigLoader interface {
    Load(ctx context.Context, path string) (*ast.File, error)
}
type ConfigCompiler interface {
    Compile(ctx context.Context, f *ast.File) (*Runtime, []Diagnostic, error)
}
```

## 11.4 Upstream

```go
type Upstream interface {
    ID() string
    RoundTrip(req *http.Request) (*http.Response, error) // context comes from req.Context()
    Healthy() bool
}
type HealthChecker interface {
    Check(ctx context.Context, u Upstream) error
}
```

## 11.5 Load balancer

```go
type Balancer interface {
    Pick(req *http.Request, candidates []Upstream) (Upstream, error)
}
```

## 11.6 Cache (streaming)

```go
type CacheMeta struct {
    Status   int
    Header   http.Header
    Stored   time.Time
    Expires  time.Time
    Size     int64
}

type Cache interface {
    Get(ctx context.Context, key string) (CacheMeta, io.ReadCloser, bool, error)
    Put(ctx context.Context, key string, meta CacheMeta) (CacheWriter, error)
    Delete(ctx context.Context, key string) error
}

type CacheWriter interface {
    io.Writer
    Commit() error // makes the entry visible atomically
    Abort() error
}
```

## 11.7 Certificates

```go
type CertificateSource interface {
    // Must not block on network issuance. Returns a cached certificate or an error.
    GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error)
}
type CertificateManager interface {
    CertificateSource
    Manage(ctx context.Context, domains []string) error // issuance/renewal in the background
    Status(domain string) (CertStatus, error)
}
```

## 11.8 Platform (split into small interfaces)

```go
type ServiceManager interface {
    Install(ctx context.Context, spec ServiceSpec) error
    Remove(ctx context.Context) error
}
type Signals interface {
    Notify() <-chan ControlEvent // reload, stop, reopen, upgrade
}
type Privileges interface {
    Drop(user, group string) error
}
type ListenerInheritance interface {
    Inherited() ([]net.Listener, []net.PacketConn, error)
    PassTo(cmd *exec.Cmd, ls []net.Listener) error
}
type ControlTransport interface {
    Listen() (net.Listener, error)
    Dial(ctx context.Context) (net.Conn, error)
}
```

# 12. Project Structure (revised)

```text
lotus/
├── cmd/lotus/main.go
├── internal/
│   ├── runtime/            # lifecycle, Runtime compile/publish, reload, upgrade
│   ├── config/
│   │   ├── lexer/ parser/ ast/ validator/ formatter/ compiler/ migrate/
│   ├── control/            # local control channel server + client
│   ├── listener/           # TCP/UDP listeners, PROXY protocol, connection limits
│   ├── server/             # net/http + quic-go wiring, protocol configuration
│   ├── router/
│   ├── vhost/
│   ├── middleware/
│   ├── fspath/             # the single safe path resolver (§4.4)
│   ├── staticfiles/
│   ├── fastcgi/            # protocol client
│   ├── php/                # PHP integration, templates, Windows php-cgi supervisor
│   ├── htaccess/
│   ├── proxy/
│   ├── loadbalancer/
│   ├── health/
│   ├── cache/
│   ├── compression/
│   ├── security/           # ip rules, rate limits, request validation, client IP
│   ├── authentication/
│   ├── certificates/       # TLS config, ACME integration, storage
│   ├── logging/
│   ├── metrics/
│   ├── adminapi/
│   ├── extensions/
│   │   ├── wasm/ rpc/ registry/
│   ├── compatibility/
│   │   ├── nginx/ apache/
│   └── platform/
│       ├── linux/ freebsd/ windows/ unix/ common/
├── pkg/
│   ├── sdk/                # extension SDK (Go, Wasm guest, gRPC protos)
│   ├── client/             # control channel / admin API client
│   └── version/
├── extensions/examples/
├── configs/
│   ├── examples/
│   └── templates/          # wordpress, laravel, symfony, drupal, joomla
├── packaging/ (deb, rpm, systemd, freebsd, windows)
├── docs/ (architecture, configuration, compatibility, extensions, security, operations)
├── tests/ (integration, compatibility, security, performance, fuzz, fixtures)
├── go.mod, go.sum, Makefile, README.md, SECURITY.md, LICENSE
```

There are no `protocol/http1` or `protocol/http2` packages (§1.1).

# 13. Performance Engineering

## 13.1 Concurrency
- One goroutine per connection/stream (Go model). Bounded where it matters: listener connection limits, per-upstream concurrency, compression semaphore, background job pools.
- Backpressure: when limits are reached, return `503` with `Retry-After`. Never queue without limit.
- No global locks on the request path (§3.3).
- Goroutine leak checks in tests (`go.uber.org/goleak`).

## 13.2 Memory and GC
- `runtime.memory_limit` sets `GOMEMLIMIT`. Defaults are derived from cgroup limits when they exist.
- Caches are capped in bytes and store bodies without pointers (§4.8.2).
- Large responses are streamed; buffers are reused with `sync.Pool` only where profiling shows a benefit.
- p99 latency under GC pressure is a benchmark scenario (§15).

## 13.3 Network
Keep-alive, upstream connection pools, TLS session resumption, `sendfile`/`TransmitFile` (§4.4), `SO_REUSEPORT` where available, configurable TCP keep-alive and buffer sizes.

## 13.4 Measurement
Benchmarks against Nginx, Apache and Caddy on identical hardware, OS, TLS settings, payloads and backends. Measured: RPS, median/p95/p99 latency, throughput, CPU, RSS, connections, TLS handshakes/s, static, proxy, PHP-FPM.

⚑ **Decision.** The benchmark harness exists from **Phase 1**, and every phase records a baseline. A regression of more than 5% p99 or RPS against the previous baseline blocks the release unless it's explained and accepted.

# 14. Security Architecture

Mandatory: safe configuration parsing; no code execution from configuration; one path resolver for every engine; symlink policy; smuggling defenses (§4.10.2); TLS validation for upstreams; secure defaults; control/admin channel protection; extension capabilities; secret redaction; resource limits; documented per-site isolation limits (§10.3); safe logging (control characters escaped); dependency vulnerability checks (`govulncheck`) in CI.

## 14.1 Secure defaults (summary)

| Setting | Default |
|---|---|
| Directory listing | off |
| Dotfiles | denied (except `/.well-known/`) |
| Symlinks | `owner_match` |
| TLS minimum | 1.2 |
| Admin API / metrics | off |
| `.htaccess` | off |
| On-demand TLS | off |
| Upstream TLS verification | on |
| Server version header | product name only, no version |
| Max request body | 10 MiB |

## 14.2 Security testing
Fuzzing (config parser, FastCGI response parser, `.htaccess` parser, Nginx/Apache parsers, PROXY protocol); smuggling test suite against proxy and FastCGI paths; path traversal and symlink escape suites (including Windows-specific: `\`, `::$DATA`, 8.3 short names, reserved device names); TLS configuration tests; auth tests; rate limit tests; resource exhaustion (slowloris, large headers, many streams on HTTP/2, rapid reset); extension isolation tests.

`SECURITY.md` defines how vulnerabilities are reported, the response deadlines and the disclosure process.

# 15. Testing Strategy

## 15.1 Unit tests
Config lexer/parser/validator/compiler, router precedence (table-driven against §4.3.3), vhost matching, path resolver, rewrite engine, cache, FastCGI protocol, proxy, load balancers, certificate selection, compression negotiation, security policies, logging.

## 15.2 Integration tests
HTTP, HTTPS, HTTP/2, HTTP/3, multiple vhosts, PHP-FPM (real PHP in containers), Windows php-cgi, reverse proxy, WebSocket, static files, cache, auth, rate limiting, reload under load (no failed requests), binary upgrade (no failed requests on Linux/FreeBSD).

## 15.3 Cross-platform
CI matrix: Linux (Tier 1, full suite with `-race`), FreeBSD and Windows (Tier 2 suite). Service install, permissions, sockets/pipes, TLS, graceful shutdown.

## 15.4 Compatibility
Application suites: WordPress (install, permalinks, uploads, admin), Laravel, Symfony, Drupal, Joomla, run with the shipped templates and with imported `.htaccess`. Differential tests compare Nginx/Apache with LOTUS for the same requests under equivalent configurations: status, headers that matter, body hash.

## 15.5 Reliability
72-hour soak tests, high concurrency, 1,000 reloads in a row under load, upstream failures, certificate renewal (with Pebble ACME test server), memory/goroutine leak detection, disk full, FD exhaustion, abrupt shutdown recovery.

## 15.6 Quality gates (every release candidate)
Unit, integration, `-race`, `go vet` + `staticcheck`, `govulncheck`, security suite, fuzz regression corpus, platform builds, compatibility regression, benchmark regression (§13.4).

A server that starts and serves an HTML page is not production-ready because of that.

# 16. Benchmarking

Scenarios: static 1 KB / 100 KB / 10 MB; reverse proxy; PHP-FPM hello world and WordPress home page; HTTPS TLS 1.3 handshake rate; HTTP/2 multiplexing; HTTP/3; WebSocket concurrency; cache warm/cold; 10k+ concurrent connections; p99 under GC pressure with a 1 GiB in-memory cache; throughput after 100 reloads.

Tools: wrk2 (fixed request rate, accurate latency), h2load, k6, vegeta, custom Go tools. The load generator runs on a separate machine.

The first numeric baselines are recorded at the end of Phase 1. Targets are set from measured results, not assumptions.

# 17. Development Roadmap (revised)

Each phase ends with a usable deliverable, a benchmark baseline and fuzz corpora for anything new it parses.

### Phase 0 — Specifications (before code)
EBNF grammar; routing precedence document; control channel protocol; directive documentation template; governance and support policy; team and timeline estimate; CI skeleton; `LICENSE` (Apache-2.0).

### Phase 1 — Foundation
Project skeleton, runtime + `Runtime` publication, CLI, control channel, config lexer/parser/validator/formatter, listeners, HTTP/1.1 **and HTTP/2**, manual TLS + SNI, vhosts, static files (with sidecars), path resolver, access/error logs, graceful shutdown, privilege model (Linux), **benchmark harness**, **parser fuzzing**.
*Deliverable:* Hosts multiple static sites over HTTPS on Linux.

### Phase 2 — Request Processing
Router (§4.3.3), middleware chain, rewrites, redirects, error pages, request validation, limits, trusted proxies / real IP, graceful reload, `config explain`, **basic Prometheus metrics**, log reopen.
*Deliverable:* A configurable general-purpose HTTP server.

### Phase 3 — PHP Hosting
FastCGI protocol, PHP-FPM integration, multiple backends, safe script resolution, application templates, **`.htaccess` PHP-application subset**, Windows php-cgi supervisor (Tier 2), application compatibility tests.
*Deliverable:* Hosts WordPress, Laravel, Symfony, Drupal and Joomla.

### Phase 4 — Reverse Proxy and Load Balancing
Proxy, HTTPS/Unix upstreams, WebSocket, balancers, health checks, pooling, buffering.
*Deliverable:* An application gateway.

### Phase 5 — Automatic TLS and HTTP/3
ACME (HTTP-01, TLS-ALPN-01, DNS-01 with three providers), renewal, certificate storage, HTTP/3 + Alt-Svc, advanced TLS settings, binary upgrade.
*Deliverable:* Zero-touch HTTPS and modern protocols.

### Phase 6 — Caching and Compression
Memory and filesystem caches, proxy cache, invalidation, dynamic compression, performance tuning.
*Deliverable:* A performance-tested server.

### Phase 7 — Security Hardening
Rate limiting, connection limits, auth (Basic, mTLS), policies, audit log, admin API, full security suite, external security review, release packaging (deb, rpm, MSI, FreeBSD pkg) and service integration on all three platforms.
*Deliverable:* A hardened server. **→ 1.0 release candidate.**

### Phase 8 — Extensions (1.x)
Wasm middleware runtime, out-of-process extension host, SDK, capabilities, signing, examples.

### Phase 9 — Full Compatibility (1.x)
Nginx and Apache translators, full `.htaccess` coverage, differential test suite.

### Phase 10 — Platform Expansion (1.x)
FreeBSD and Windows promoted toward Tier 1, Windows binary upgrade, packaging for more platforms.

Phase 1–7 work may overlap where items don't depend on each other. A feature is not "done" until its tests, documentation and benchmark entry exist.

# 18. Installation and Distribution

- **Linux:** `.deb`, `.rpm`, static binary; systemd unit with `CAP_NET_BIND_SERVICE`; `lotus` user and group; `/etc/lotus`, `/var/log/lotus`, `/run/lotus`, `/var/lib/lotus` (certificates, cache).
- **Windows:** signed MSI and zip; service install/remove; Event Log for errors; `%ProgramFiles%\Lotus` for config, logs and data.
- **FreeBSD:** port/pkg; `rc.d` script; `/usr/local/etc/lotus`.

Installers never overwrite existing web server configuration. LOTUS runs alongside Nginx and Apache on different listeners. Package upgrades keep user configuration and run `lotus config validate` before restarting.

# 19. Documentation Requirements

Installation, quick start, architecture, configuration language (grammar + every directive), protocols, virtual hosting, routing precedence, PHP hosting and templates, reverse proxy, TLS/ACME, security and isolation limits, caching, compression, logging, metrics, CLI, control channel and admin API, extensions, compatibility (per-directive level), `.htaccess`, platform differences (§2.3), troubleshooting, performance tuning, upgrades.

Every directive has documentation of its type, default, valid contexts, whether it accepts request-time placeholders, behavior and security implications. Documentation is generated from the directive registry so it can't drift from the code.

# 20. Release Strategy

| Release | Contents | Platforms |
|---|---|---|
| **Alpha** | Phases 1–2: static sites, vhosts, routing, manual TLS, HTTP/1.1 + HTTP/2, reload | Linux |
| **Beta** | + Phases 3–5: PHP hosting, `.htaccess` subset, reverse proxy, ACME, HTTP/3 | Linux; FreeBSD/Windows preview |
| **Release Candidate** | + Phases 6–7: caching, compression, security hardening, admin API; external security review; 72 h soak | Linux Tier 1; FreeBSD/Windows Tier 2 |
| **1.0** | RC with all §21 criteria met | As above |
| **1.x** | Extensions, full compatibility translators, platform expansion | — |

# 21. 1.0 Acceptance Criteria

⚑ **Decision.** 1.0 means the core server; the items in §22 are planned for 1.x.

- [ ] Independent installation (deb, rpm, Windows MSI, FreeBSD pkg).
- [ ] Native configuration language with grammar, validator, formatter, `explain`, `migrate`.
- [ ] HTTP/1.1, HTTP/2, HTTP/3, HTTPS.
- [ ] Multiple virtual hosts with SNI.
- [ ] Static file serving with sidecar compression.
- [ ] PHP-FPM integration and templates for the five named applications; Windows php-cgi supervisor.
- [ ] `.htaccess` PHP-application subset.
- [ ] Reverse proxy, WebSocket, load balancing, health checks.
- [ ] Automatic certificate management (ACME).
- [ ] Routing and rewriting with documented precedence.
- [ ] Caching and compression.
- [ ] Access controls, authentication, rate limiting, trusted-proxy client IP.
- [ ] Structured logging and Prometheus metrics.
- [ ] Graceful shutdown and graceful reload; binary upgrade on Linux/FreeBSD.
- [ ] Local control channel; optional admin API.
- [ ] Built-in module architecture.
- [ ] Linux Tier 1; FreeBSD and Windows Tier 2 with documented limitations.
- [ ] Automated tests and quality gates (§15.6).
- [ ] Reproducible benchmarks published with methodology.
- [ ] Security testing and an external security review.
- [ ] Installation, operation and upgrade documentation.

# 22. Post-1.0 Criteria (1.x)

- [ ] External extension framework (Wasm + out-of-process) with SDK.
- [ ] Nginx configuration translator.
- [ ] Apache configuration translator.
- [ ] Full supported-directive `.htaccess` coverage.
- [ ] FreeBSD/Windows promoted to Tier 1; Windows binary upgrade.
- [ ] Optional WAF integration.

# 23. Implementation Rules

1. LOTUS is an independent product.
2. Use standard and established libraries for protocols and cryptography (§1.1). Never use Nginx or Apache as the engine, and never copy their code.
3. Keep internal modules independent; keep interfaces small.
4. Write the specification (grammar, protocol, precedence) before implementing a major subsystem.
5. No unnecessary abstractions.
6. No unbounded goroutines, queues, caches or buffers.
7. Never silently ignore unsupported configuration directives.
8. Never activate invalid configuration; keep the running configuration when reload fails.
9. Every URL-to-file mapping goes through `internal/fspath`.
10. Test on every supported platform tier.
11. Benchmark before claiming performance; track regressions every phase.
12. Document security-sensitive behavior and isolation limits.
13. Keep stable directives backward compatible; change semantics only with a new `lotus_config_version`.
14. Extensions are isolated and limited by capabilities.
15. Don't claim Nginx/Apache compatibility without conformance evidence.
16. A feature is finished when its tests, documentation and benchmark entry are finished.

# 24. Project Governance (to be completed in Phase 0)

- ⚑ **License: Apache-2.0.** All planned dependencies are compatible (`quic-go` MIT, `wazero` Apache-2.0, `klauspost/compress` BSD/Apache, `andybalholm/brotli` MIT, `certmagic` Apache-2.0). CI checks the licenses of new dependencies.
- **Versioning:** SemVer for the binary; integer `lotus_config_version` for the configuration language; SemVer for the extension API.
- **Support policy:** support window for each minor release; security fixes for the current and previous minor.
- **Team and timeline:** estimate per phase after Phase 0, using the size of the Phase 0 specifications.

# 25. Final Product Definition

**LOTUS — Laika Optimized Transport & Unified Server** is an independent, cross-platform, modular web server written in Go. It builds on Go's proven HTTP, TLS and QUIC libraries, and provides its own configuration language, routing, virtual hosting, PHP/FastCGI hosting, reverse proxy, load balancing, automatic TLS, caching, security architecture and control plane.

Its focus is PHP and shared hosting, and migration from Apache and Nginx. Its long-term goal is to be a credible alternative to Nginx and Apache, with compatibility proven by tests.

The project puts correctness, security, reliability and measurable performance ahead of feature count.

---

# 26. Change Log (v1.0 → v1.1)

| Area | Change | Section |
|---|---|---|
| Name | Product renamed from "LWS — Laika Web Server" to **LOTUS — Laika Optimized Transport & Unified Server**. The binary is `lotus`, the module `github.com/laikait/lotus`, configuration files use the `.conf` extension and `lotus_config_version`, diagnostic codes start with `LOTUS-CFG-`, and the control protocol is `lotus.control.v1`. | all |
| Protocol layer | States that `net/http`, `crypto/tls` and `quic-go` are the protocol layer; removed `protocol/http1`, `protocol/http2` | §1.1, §12 |
| Positioning | Added differentiators vs Caddy and the others; added non-goals | §1.2, §1.4 |
| Platforms | Tier model; Linux Tier 1, FreeBSD/Windows Tier 2 at 1.0; platform difference table | §2 |
| Windows PHP | LOTUS supervises a `php-cgi.exe` pool | §4.5.4 |
| Control plane | New local control channel; admin API separated | §9 |
| Process model | `workers` replaced by `runtime.max_procs` / `memory_limit`; privilege model; binary upgrade; isolation limits | §4.13, §10 |
| Config language | Load-time `${var}` vs request-time `{placeholder}`; units; include and merge rules; `lotus_config_version`; `migrate` | §5 |
| Routing | Precedence defined, matching Nginx `location` | §4.3.3 |
| Interfaces | Streaming cache API; `ClientHelloInfo`-based certificates that don't block; split platform interfaces; narrow module host | §11 |
| Extensions | Wasm (wazero) for per-request middleware; RPC for handlers/async hooks | §7 |
| Missing features | Trusted proxies / PROXY protocol, sidecar compression, `100-continue`, compression vs Range, Alt-Svc, log reopen, ACME storage/locking, OCSP downgraded | §4 |
| Go runtime | GOMEMLIMIT, cache storage without pointers, sendfile preservation, backpressure | §13 |
| Roadmap | Phase 0 added; HTTP/2 → Phase 1; metrics → Phase 2; `.htaccess` subset → Phase 3; benchmarks + fuzzing from Phase 1; extensions/compat/platform → 1.x | §17 |
| Acceptance | 1.0 vs 1.x criteria split, matching §20 | §21, §22 |
| Governance | License, versioning, support policy, staffing placeholders | §24 |
