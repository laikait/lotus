# Roadmap Effort Estimates (Phase 0, first pass)

**Status:** First estimate, based on the v1.1 spec and the Phase 0 design documents. **Re-estimate at the end of Phase 1** using measured progress.

Units: **person-weeks (pw)** of senior Go engineering, including tests and documentation for each feature (spec rule 16). Ranges show uncertainty.

| Phase | Scope (spec §17) | Estimate (pw) | Largest risks |
|---|---|---|---|
| 0 | Specifications, governance, CI skeleton | 3–4 | — (mostly done) |
| 1 | Foundation: config language, runtime, control channel, CLI, listeners, HTTP/1.1+2, TLS/SNI, vhosts, static files, path resolver, logs, privilege drop, benchmark harness, fuzzing | 24–32 | Config language toolchain (parser + validator + formatter that keeps comments) |
| 2 | Router, middleware, rewrites, validation, limits, trusted proxies, reload, `explain`, metrics | 18–24 | Reload under load with zero errors |
| 3 | FastCGI, PHP-FPM, templates for 5 apps, `.htaccess` subset, Windows php-cgi supervisor | 22–30 | `.htaccess` rewrite behavior matching Apache; WordPress/Drupal edge cases |
| 4 | Reverse proxy, balancers, health checks, WebSocket, buffering | 14–20 | Buffering/streaming correctness, retry semantics |
| 5 | ACME (certmagic), DNS-01, HTTP/3, binary upgrade | 12–18 | Binary upgrade on Linux + FreeBSD; QUIC tuning |
| 6 | Caches, proxy cache, compression, performance work | 14–20 | RFC 9111 correctness; GC behavior with large caches |
| 7 | Security hardening, auth, admin API, security suite, packaging on 3 platforms | 20–28 | Findings from the external security review |
| **Total to 1.0** | | **127–176 pw** | |

## Calendar scenarios

Calendar time is not effort ÷ people. The table assumes 75% of each person's time goes to roadmap work (the rest is reviews, meetings, support), and that phases overlap only partly.

| Team | Calendar to 1.0 RC | Notes |
|---|---|---|
| 2 senior Go engineers | ~20–28 months | Little parallel work possible; a high bus-factor risk |
| **3 senior + 1 QA/DevOps** | **~13–18 months** | Recommended minimum. QA/DevOps owns CI, the platform matrix, benchmarks and packaging. |
| 5 senior + 1 QA/DevOps + part-time security | ~9–12 months | Phases 3/4 and 5/6 can run in parallel; coordination costs more |

Not included: the external security review fee, 1.x work (extensions, full Nginx/Apache translators, platform expansion), and hiring/onboarding time.

## Milestones (3 + 1 scenario, from project start)

| Milestone | Target |
|---|---|
| Phase 0 complete | Month 1 |
| Alpha (Phases 1–2) | Month 6–7 |
| Beta (Phases 3–5) | Month 11–13 |
| Release Candidate (Phases 6–7) | Month 13–18 |
| 1.0 | RC + 4–6 weeks of soak testing and fixes |
