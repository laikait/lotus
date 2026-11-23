# Review: LOTUS Technical Specification

## Context
Review of the finalized LOTUS Technical Specification (v1.0). Findings are ordered by importance. The resolutions are applied in `../LOTUS-Technical-Specification-v1.1.md`.

---

## Overall verdict
The spec is solid on principles: security-first, honest about compatibility claims, phased delivery, uses Go's standard library and existing crypto, and doesn't depend on Go's `plugin` package. Its main weaknesses are **scope**, **a few internal contradictions**, and **missing control-plane and runtime design**. As written, 1.0 asks for most of Nginx + Apache + Caddy + certbot + a WAF + a plugin platform, on three OS families.

---

## 1. Critical issues

**1.1 1.0 scope contradicts itself.**
§19 says full Nginx/Apache compatibility is *not* required for 1.0. But §21 (acceptance criteria) requires the Nginx layer, the Apache layer, `.htaccess`, HTTP/3, the extension framework and Windows + FreeBSD support. → Pick one. I recommend a short MVP list (static files, vhosts, PHP-FPM, reverse proxy, automatic TLS, HTTP/1.1 + HTTP/2, Linux), with everything else moved to 1.x.

**1.2 "Own HTTP engine" vs "use net/http".**
§1 says LOTUS has its *own* HTTP engine. §4.1 says to use Go's standard library. The project tree (`protocol/http1`, `protocol/http2`) suggests writing parsers by hand. → State it plainly: `net/http` (with `x/net/http2` / Go's `Protocols` for h2c) and `quic-go` are the protocol layer, and LOTUS owns everything above them. Delete `protocol/http1` and `protocol/http2`. This also settles the request-smuggling question: parsing is done the way `net/http` does it. The trade-off is less control over raw parsing (header order, raw request line), and the spec should say that.

**1.3 No positioning against Caddy.**
Caddy is already a modular Go web server with automatic TLS. The spec never says why LOTUS should exist next to it. Likely answers: first-class PHP-FPM hosting, `.htaccess`/Apache migration, shared-hosting isolation, LPanel. → Add a "Differentiation & Non-goals" section. It should drive priorities, for example moving `.htaccess` earlier if PHP shared hosting is the main use.

**1.4 Missing control-plane design.**
`lotus reload`, `lotus status` and `lotus cache purge` need to reach the running process, and nothing says how: signals on Unix, but on Windows? → Specify a local control channel (Unix socket / Windows named pipe, with ACLs). Make it separate from the optional network admin API, and have the CLI use it.

**1.5 Process and privilege model is undefined.**
- `workers auto` in the example config has no meaning in a single-process Go server. Either define it (GOMAXPROCS?) or remove it.
- Binding :80/:443 without root needs a stated approach: `CAP_NET_BIND_SERVICE`, systemd socket activation, or bind then drop privileges. Go's `setuid` after startup has caveats.
- Config reload (atomic handler swap via `atomic.Pointer`, keeping the same listeners) is different from **binary upgrade** (passing listener file descriptors to a new process, or `SO_REUSEPORT`, which doesn't exist on Windows). The spec only describes config reload.
- Multi-tenant isolation: LOTUS reads static files as *its own* user for every site, so per-site filesystem isolation for static content is weak. PHP-FPM pools only isolate PHP. Say this explicitly (§13.1 hints at it).

**1.6 Windows has no PHP-FPM.**
On Windows, PHP runs as `php-cgi.exe` FastCGI processes, which someone has to spawn and supervise. "Full support" on Windows Server therefore means either LOTUS manages a php-cgi process pool or PHP on Windows is out of scope. → Decide which.

## 2. Design issues

**2.1 Config language.**
- `$uri` (filled in per request) and config variables (filled in when the config loads) both use `$`. The grammar must keep them apart.
- How a `site` binds to particular `listen` blocks, and how per-site certificates are assigned, isn't shown.
- Define the type system early: sizes (`100mb`: is that 10^6 or 2^20?), durations, and the rules for merging included files.
- Add `lotus config migrate`, since the spec promises versioned semantics.

**2.2 Routing precedence must be fixed before the Nginx translator exists.**
If native precedence differs from Nginx `location` rules (exact → longest prefix with `^~` → regex in file order → longest prefix), every translation becomes lossy. Either mirror Nginx's model or document how translation maps between the two.

**2.3 Interfaces (§11):**
- `Cache.Get` returns a full in-memory `*CacheEntry`. That conflicts with "don't buffer large bodies" and the filesystem cache. → Return metadata plus an `io.ReadCloser` body (and a streaming writer for `Set`).
- `CertificateProvider.GetCertificate(ctx, domain)` should take `*tls.ClientHelloInfo` so it plugs straight into `tls.Config.GetCertificate`. It also needs a non-blocking path, because ACME issuance must not run inside a TLS handshake.
- `Platform` only covers service installation. The responsibilities in §2.2 (signals, privileges, file locking) need their own small interfaces.
- `Module.Initialize(deps Dependencies)` risks turning into a do-everything dependency container. Keep `Dependencies` narrow.

**2.4 Out-of-process extensions as per-request middleware.**
An RPC round-trip on every request adds latency and becomes a point of failure. → Use out-of-process for handlers and async hooks. Move the **Wasm (wazero, pure Go)** runtime up from "later option" to the main model for request and response middleware. Also consider Caddy-style compile-time modules for trusted extensions.

**2.5 Go runtime specifics missing from §12:**
- GC tail latency with large in-memory caches. Recommend `GOMEMLIMIT`, pointer-free storage that the GC doesn't have to scan (byte slabs), and capping the cache in bytes.
- Rely on `io.Copy` to use sendfile (Linux) and TransmitFile (Windows) automatically. Don't wrap writers in a way that breaks this.
- Slowloris: map `header_timeout` to `ReadHeaderTimeout`, and limit connections at the listener.

## 3. Missing features / requirements

- **Trusted proxies and real client IP** (X-Forwarded-For / Forwarded) and **PROXY protocol**. IP allowlists, rate limits and logs are wrong behind a load balancer without them.
- **Pre-compressed static files** (`.br`/`.gz` sidecar files, like nginx's `gzip_static`). Go's Brotli encoder (andybalholm/brotli) is slow, so this matters more than usual.
- `Expect: 100-continue`, compression vs Range-request interaction, `Alt-Svc` advertising for HTTP/3, UDP buffer tuning for QUIC.
- Log rotation: reopen files on signal on Unix; a Windows equivalent.
- ACME in multi-instance deployments: shared certificate storage, locking, CA rate limits. DNS-01 needs one integration per DNS provider, so define the provider scope. Consider building on `certmagic` or `lego` instead of writing ACME from scratch.
- **OCSP**: Let's Encrypt shut down its OCSP service in 2025. Drop or downgrade the OCSP stapling item.
- **Explicit non-goals**: CGI, SSI, mail proxy, stream/TCP proxying (nginx `stream`), clustering, etc.
- Project metadata: license, governance, team size and timeline, how the config language is versioned, a support policy.

## 4. Roadmap issues

- **Metrics/Prometheus and the admin API don't appear in any phase.** Put basic metrics in Phase 2.
- **HTTP/2 shows up in Phase 5**, but `net/http` turns it on with TLS from Phase 1. Treat it as Phase 1 (with a switch to disable it) and test it there.
- **Benchmark harness and fuzzing** should start in Phase 1 (config parser) and Phase 2 (HTTP limits). §12 itself says performance is "not an optimization phase at the end".
- **PHP apps in Phase 3, `.htaccess` in Phase 9.** That's fine if WordPress and Laravel run on native rules (ship templates for them). If Apache migration is the main selling point, `.htaccess` should move earlier.
- Rule 18 ("finish each phase completely") together with 10 phases means a very late first useful release. Define alpha, beta and 1.0 by *capabilities*, not by phase numbers.

---

