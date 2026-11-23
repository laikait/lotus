# LOTUS — Laika Optimized Transport & Unified Server

**A next-generation, cross-platform web server.**

An independent, cross-platform web server written in Go. It's built for PHP and shared hosting, migration from Apache and Nginx, and control-panel integration.

> **Status: Phase 1 (foundation), in progress.** LOTUS can already serve static websites, but it is not ready for production.

## What works today

- **Configuration:** the `.lotus` language (parser, includes, variables, `env()`/`file()`), a directive registry, validation with line/column errors, and `lotus config validate` / `lotus config inspect` (secrets redacted).
- **Serving:** `lotus start` runs HTTP/1.1 and HTTPS (HTTP/2 included), virtual hosts with wildcard names, SNI certificate selection (`tls manual`), static files (ETag, Range, HEAD, index files, no directory listings), security headers, access logs (Combined format), connection and body limits, and graceful shutdown.
- **Routing:** `route` blocks follow the Nginx `location` model (exact, prefix, `^~`, regex, named routes), with `return` (redirects and fixed responses), `try_files` (including `=404` and `@named` fallbacks, at most 10 internal redirects), `match` blocks (method, header, query, cookie, client IP, scheme, host), per-route `root` and `max_request_body`, and request-time placeholders such as `{uri}` and `{re.1}`. See `configs/examples/routes.lotus`.
- **Safety:** every URL-to-file mapping goes through one resolver that blocks traversal, hidden files, symlink escapes and (on Windows) device names.

**Not implemented yet.** A configuration that uses any of these is refused at startup with a clear message, never silently ignored: the `php` route action and PHP/FastCGI, reverse proxy, automatic certificates (ACME), HTTP/3, `trusted_proxies`, per-site timeouts, the control channel.

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
