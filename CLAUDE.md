# LOTUS: notes for Claude (and for a new machine)

LOTUS is a web server written in Go: module `github.com/laikait/lotus`, binary `lotus`, config files `.conf`. Product name: **LOTUS** (Laika Optimized Transport & Unified Server). Never write "LWS" or "Laika Web Server" in code, docs or messages; the project was renamed before release.

## How to work with the owner

- The owner drives the project step by step, usually by saying "go next". Do the recommended next step; don't ask what to do.
- End every substantive reply with a concrete **"Next:"** recommendation.
- **The owner commits and pushes. Never commit or push.** Give a suggested commit message instead.
- Use they/them for people whose pronouns are not stated.
- Keep personal details (names, e-mail addresses) out of requests to outside services and out of the repo.
- Report honestly: say what was tested and what was not (the Windows service has been run only by the owner, by hand).

## Decisions already made (don't re-ask)

- Positioning: PHP and shared hosting, Apache/Nginx migration.
- 1.0 is the core server. Linux is Tier 1; FreeBSD and Windows are Tier 2.
- PHP on Windows through a managed `php-cgi` pool run by LOTUS.
- A `.htaccess` subset; Nginx `location` precedence for routing.
- Extensions: Wasm (wazero) plus out-of-process.
- ACME through certmagic. Licence Apache-2.0.
- Windows paths: program `C:\Program Files\Lotus`, config `C:\Program Files\Lotus\main.conf`, web directory `C:\www`. Linux: config `/etc/lotus/main.conf`, binary `/usr/local/bin/lotus`, web directory `/var/www/public_html` (to be created by the future `.deb`/`.rpm`).

Documents: `docs/architecture/`, `docs/configuration/` (generated directive reference in `docs/configuration/directives/`, made by `make docs`), `docs/roadmap/1.0-checklist.md` (what 1.0 still needs), `docs/benchmarks.md`, `docs/windows-installer.md`, `docs/code-signing.md`.

## Setting up on a new machine

1. Install Go (version in `go.mod`) and Git. Optional: Inno Setup 6/7 (Windows installer), WSL with Ubuntu (Linux test run), `staticcheck` (`go install honnef.co/go/tools/cmd/staticcheck@v0.8.2`), XAMPP's `php-cgi` (PHP tests).
2. `go build ./... && go test ./...`
3. Before handing work over, the full check is: `gofmt -l .` (prints nothing), `go vet ./...`, cross-vet with `GOOS=linux|freebsd|darwin|windows`, `staticcheck ./...`, `go test ./...`, and `bash scripts/test-linux-wsl.sh` for the Linux run (it sometimes fails with "Cannot allocate memory" in WSL; run it again). `-race` is left to CI.
4. Useful commands: `make build`, `make docs` (regenerates the directive reference; a test fails if the pages are stale), `make bench` (`tools/bench`), `make dist` (release archives), `scripts\build-installer.ps1` (Windows installer).

## Gotchas

- Edit line endings with care: files are LF. After a `git stash` round trip on Windows a file can come back as CRLF and fail `gofmt -l`; fix with `sed -i 's/\r$//' FILE`.
- The Bash tool of Claude Code drops backslashes and breaks on some quotes in heredocs; write files with the Write/Edit tools.
- `gh` is not installed on the owner's machine; use `curl` against the GitHub API if needed.
- A WSL run needs the `go` command on PATH for tests that build the server; those tests skip themselves otherwise.
- Windows file access is slow (antivirus); see the profile in `docs/benchmarks.md`.
- `cmd/lotus/rsrc_windows_amd64.syso` is the logo as a Windows resource; regenerate it with `go run ./tools/mkicon -syso cmd/lotus/rsrc_windows_amd64.syso`. The logo is a placeholder.

## State at the last session (2026-11-26)

Done: config language, parser, validator, formatter, migrate; static files, routing, proxy, cache, compression, TLS and ACME, PHP/FastCGI (including the managed pool), `.htaccess` subset, control channel, metrics, HTTP/3, Nginx translator (`lotus compat nginx convert`), service install (systemd, rc.d, Windows service), release scripts and workflow, generated directive reference with tested examples, benchmark harness, Windows installer (Inno Setup, written but only partly tested).

Open items:

1. **Windows service control socket (mostly closed 2026-11-26).** The service runs as SYSTEM and its socket gives `Users` read-only, so a normal shell can't connect; `lotus reload` works from an Administrator shell (the owner confirmed). Done: the CLI now prints the real error and says to use an Administrator shell, and `removeStale` leaves a socket it can't connect to because of permissions. Still open: decide who may connect (Windows `restrictSocket` is a no-op; today Administrators and SYSTEM only), and the new CLI message has not been run from a rebuilt exe.
1b. **`lotus site add|list|remove`** (cmd/lotus/site.go, 2026-11-26): writes `sites.d/NAME.conf` next to the main file, validates, rolls back if invalid; the installer's `main.conf` includes `sites.d/*.conf`. Needs a docs page and a rebuilt installer run.
1c. **Web admin** (plan: C:\Users\Showket Ahmed\.claude\plans\ok-i-want-to-sorted-eclipse.md; docs/architecture/admin.md). Step 1 built 2026-11-26: `admin { address ... }` block, `internal/admin` (argon2id accounts, sessions, CSRF, lockout, embedded UI, dashboard + Reload), `lotus admin passwd|users`, installer wizard page + Start Menu entry. Step 1 confirmed working on the owner's Windows install (sign-in, dashboard, Reload). Step 2 built 2026-11-26 (`internal/sitefile` shared with `lotus site`; Sites page: add/edit/delete, validate whole config, roll back, reload; hand-edited files are read-only; API `/v1/site-files`): unit tests and a run of the real binary over HTTP pass; the Sites page itself was never opened in a browser; HTTPS mode not run. Step 3 built 2026-11-26: Cache (status + purge), PHP (read-only pools via `Server.PHPPools`), Server (reload, reopen logs; the owner asked to remove Stop from the panel, so there is none on purpose); unit tests and a run of the real binary pass; pages never opened in a browser. Certificate renewal was left out on purpose (certmagic `RenewCertSync` does not refresh the in-memory cache; needs a real CA or pebble to test). Step 4 built 2026-11-26: Logs viewer (admin role only; sources by id, never a path) and Configuration editor (admin role only; main.conf + sites.d/*.conf only; Check = no side effects, Save = whole-config check, `.bak`, reload, undo if refused) in `internal/sitefile/edit.go` + `internal/admin/{logs,config}.go`; docs and checklist item 15 updated (step 5 done). Unit tests and a run of the real binary pass; pages never opened in a browser. Certificate renewal built 2026-11-26 (`acme.Manager.Renew`, `Server.RenewCertificate`, control `lotus.cert.renew`, `lotus certs renew DOMAIN`, admin `POST /v1/certs/renew` + Renew button; 24 h guard `acme.MinRenewAge`; the new certificate must replace the old one in certmagic's cache, which the Pebble test `TestRenewCertificateWithPebble` caught; to run Pebble locally: `go install` pebble and pebble-challtestsrv v2.10.3 into E:\Claude Memory\work\pebble and set LOTUS_PEBBLE_DIR and LOTUS_PEBBLE_TESTDIR as the CI does). Round 2 (plan: C:\Users\Showket Ahmed\.claude\plans\ok-i-want-to-sorted-eclipse.md): A web directory follows the site name (fixed 2026-11-26, JS logic checked in node, not in a browser) and C live Traffic page (`Server.Traffic`, `/v1/traffic`, sparklines; real binary checked) are built; B site types + advanced settings is built too (2026-11-26: `internal/sitefile/template.go`, spec line `# spec: JSON` + exact-render rule, types static/php/wordpress/laravel/proxy; real binary run: a proxy site and a static site served; the form was never opened in a browser; a proxy site has no `root` because the server refuses a root that does not exist); D config history (`internal/sitefile/history.go`, `history/` folder, 20 versions per file) + Users page (`internal/admin/users_pages.go`, last-admin and own-account rules, sessions revoked) is built (2026-11-26; unit tests and a real-binary run pass; pages never opened in a browser); E PHP pools + HTTPS wizard is built (2026-11-26: managed file `sites.d/00-lotus-server.conf` in `internal/sitefile/server.go`, `internal/admin/server_settings.go`, `Pool.RecycleAll`, `Server.RestartPHPPool`, site `listeners` field; real binary with XAMPP's php-cgi served PHP, restarted the pool and kept answering; the wizard's listeners need a service restart, never tried end to end with a real CA; pages never opened in a browser). Round 2 is complete; next: the owner's browser check of every page, then the 1.0 checklist backlog. One flake seen once in the full Windows run, not reproduced in 38 reruns: `TestProcessesAreRecycledAfterMaxRequests` ("backend closed the connection"; the code it exercises was not changed by E except for a `recycle` flag); the HTTP/3 tests can fail on Windows when the OS refuses a UDP port. Open: an HTTPS run of the admin listener, a Linux run, a browser check of every page.
2. The installer has not been rebuilt and run since the changes of 2026-11-26 (path `Lotus`, `C:\www`, `PATH` option, logo).
3. `SECURITY.md` needs a public contact e-mail (not a personal one) from the owner.
4. SignPath Foundation application for code signing was to be submitted by the owner; after approval set the repository variable `SIGNPATH_ORGANIZATION_ID` and the secret `SIGNPATH_API_TOKEN` (see `docs/code-signing.md`).
5. The team and timeline (§24 of the spec) and the overdue Phase 1 re-estimate in `docs/roadmap/estimates.md`.

Backlog (from `docs/roadmap/1.0-checklist.md`): application tests and templates for WordPress, Laravel, Symfony, Drupal and Joomla (recommended next: WordPress and Laravel with XAMPP's `php-cgi`); binary upgrade; a file-system cache tier; `.deb`/`.rpm`/MSI packages; mTLS, audit log, PROXY protocol, a third DNS provider, the network admin API; a FreeBSD CI run; a 1,000-reload test and a soak test; the security suite and an external review; the module-architecture decision (recommended: 1.x); an Apache `httpd.conf` translator; an open-file cache for static files (a trade-off, not a fix).
