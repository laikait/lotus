# Local Control Channel Protocol

**Protocol version:** `lotus.control.v1`
**Status:** implemented in `internal/control` (server, client, socket handling) and `cmd/lotus` (`reload`, `stop`, `status`, `logs reopen`). §9 lists where the implementation differs from this draft.
**Spec reference:** v1.1 §9.1

The control channel is how the `lotus` CLI (and local tools such as LPA) talk to a running LOTUS instance. The optional network admin API (§9.2) exposes the same operations through the same handlers, so there is one implementation for both.

---

## 1. Transport

| Platform | Endpoint (default) | Access control |
|---|---|---|
| Linux / FreeBSD | Unix socket `/run/lotus/control.sock` | File mode `0660`, owner `root` (or `lotus`), group `lotus-admin`. The peer's identity is read with `SO_PEERCRED` (Linux) / `getpeereid` (FreeBSD). |
| Windows | Named pipe `\\.\pipe\lotus-control` | Security descriptor: full control for `BUILTIN\Administrators`, `NT SERVICE\lotus` and the `LOTUS Admins` group. Client identity from `GetNamedPipeClientProcessId` + token. Remote clients are rejected (`PIPE_REJECT_REMOTE_CLIENTS`). |
| macOS (dev) | Unix socket `$TMPDIR/lotus/control.sock` | Same as Unix. |

- The path can be set with `server { control { socket "..." } }` and `lotus --control <path>`.
- The server refuses to start if the socket's directory is writable by anyone other than root/the service user.
- If a stale socket file exists, LOTUS deletes it at startup only when nothing is listening on it (checked with a test connection).

## 2. Framing

- JSON-RPC 2.0 messages, **one per line**: UTF-8 JSON with no raw newlines, ending in `\n`.
- Maximum message size: 1 MiB. Anything larger closes the connection with error `-32600`.
- A connection may carry several requests in a row. Requests on one connection are handled in order. Notifications (requests without an `id`) aren't used by clients.
- Idle timeout: 60 s. Connections allowed at once: 16 (more are refused at accept time).

## 3. Handshake

The client's first call must be `lotus.hello`:

```json
{"jsonrpc":"2.0","id":1,"method":"lotus.hello","params":{"protocol":"lotus.control.v1","client":"lotus-cli/0.1.0"}}
```

```json
{"jsonrpc":"2.0","id":1,"result":{"protocol":"lotus.control.v1","server":"lotus/0.1.0","pid":4242,"role":"operator","methods":["lotus.status","lotus.reload", "..."]}}
```

- If the server doesn't support the requested `protocol`, it returns error `-32010` with `data.supported: [...]`.
- `role` comes from the peer's identity: `operator` for root/Administrators/`lotus-admin` members, otherwise `viewer` (only when `control.viewer_group` is configured).

## 4. Methods

Methods that change something require `operator`. Methods that only read are allowed for `viewer`.

| Method | Role | Params | Result |
|---|---|---|---|
| `lotus.hello` | any | `protocol`, `client` | see §3 |
| `lotus.status` | viewer | — | version, pid, start time, config generation, config file, listeners, site count, goroutines, memory |
| `lotus.health` | viewer | — | `{ "status": "ok" \| "degraded", "checks": [...] }` |
| `lotus.reload` | operator | `{ "config": "<path?>" }` | `{ "generation": n, "diagnostics": [...] }` or error `-32020` with diagnostics |
| `lotus.stop` | operator | `{ "graceful": true, "timeout": "30s" }` | `{}` (the server then shuts down) |
| `lotus.upgrade` | operator | `{ "binary": "<path?>" }` | `{ "new_pid": n }` (Linux/FreeBSD; `-32030` elsewhere) |
| `lotus.logs.reopen` | operator | — | `{ "reopened": n }` |
| `lotus.config.inspect` | viewer | `{ "resolved": bool, "site": "<name?>" }` | the effective configuration as JSON, **secrets redacted** |
| `lotus.config.explain` | viewer | `{ "site", "method", "url", "headers"? , "client_ip"? }` | vhost, route chain, chosen `match`, handler, internal redirect trace |
| `lotus.site.list` | viewer | — | `[ { name, aliases, listeners, tls, status } ]` |
| `lotus.site.inspect` | viewer | `{ "name" }` | site details |
| `lotus.cert.list` | viewer | — | `[ { name, domains, issuer, not_after, managed, source, site, status } ]`. `status` is `ok`, `expiring` (under 14 days), `expired` or `pending` (a certificate that is to be obtained from a CA and isn't there yet; `not_after` is absent). `source` is `file` (`tls manual`) or `acme` |
| `lotus.cert.renew` | operator | `{ "domain", "force": bool }` | `{ "scheduled": true }` |
| `lotus.cache.status` | viewer | `{ "zone"? }` | `[ { zone, entries, bytes, max_bytes, max_entry_size, hits, misses, stale, bypass, stored, evictions, purged } ]`. An unknown zone is `-32040` |
| `lotus.cache.purge` | operator | `{ "zone"?, "host"?, "path"?, "prefix"?, "all"? }` | `{ "purged": n }`. Exactly one of `path`, `prefix` and `all` (`all` can't be combined with `host`); `zone` may be left out when there is one zone. A purge that doesn't say what to remove is `-32602`, an unknown zone `-32040`. Audited |
| `lotus.module.list` | viewer | — | `[ { name, version, kind, status } ]` |
| `lotus.metrics` | viewer | `{ "format": "prometheus" \| "json" }` | metrics text or object |

Names have the form `lotus.<area>.<verb>`. New methods may be added within v1. Renaming or removing a method requires `lotus.control.v2`.

`lotus.reload` is synchronous. It returns only after the new configuration is published or rejected (spec §4.13.1 steps 1–6). Draining old connections (steps 7–8) continues afterward.

## 5. Errors

Standard JSON-RPC codes plus LOTUS codes:

| Code | Meaning | CLI exit code |
|---|---|---|
| -32700 / -32600 / -32601 / -32602 | parse error / invalid request / unknown method / invalid params | 1 |
| -32001 | permission denied (role) | 4 |
| -32010 | unsupported protocol version | 3 |
| -32020 | configuration invalid (`data.diagnostics` = same format as `config validate --json`) | 2 |
| -32021 | reload already in progress | 5 |
| -32030 | not supported on this platform | 6 |
| -32040 | not found (site, domain, cache zone) | 7 |
| (CLI only) | `lotus certs`: a certificate is pending, expired or inside the warning window | 8 |
| -32050 | internal error (details only in the server log) | 10 |

If the CLI can't connect at all, it exits with **3** ("runtime unreachable") and prints the endpoint it tried.

Error `message` text is meant for people and may change. Scripts should rely on `code` and `data`.

## 6. Security

- The channel never accepts file contents for configuration. `lotus.reload` only takes a **path**, and the server re-checks it under its own permissions using the include rules (grammar §6.5).
- No method runs commands, reads arbitrary files or returns secrets.
- Every `operator` call is written to the audit log with peer uid/SID, pid, method and outcome.
- Request parameters are validated against JSON schemas in `internal/control/schema/`.

## 7. Signals (Unix compatibility)

| Signal | Same as |
|---|---|
| `SIGHUP` | `lotus.reload` (errors are only logged) |
| `SIGTERM`, `SIGINT` | `lotus.stop` graceful |
| `SIGQUIT` | immediate stop |
| `SIGUSR1` | `lotus.logs.reopen` |
| `SIGUSR2` | `lotus.upgrade` |

Windows service control: `SERVICE_CONTROL_STOP` → graceful stop. `SERVICE_CONTROL_PARAMCHANGE` → reload.

## 8. Testing

- Protocol tests use an in-memory `net.Pipe` transport, with golden request/response files in `internal/control/testdata/`.
- Permission tests per platform: a connection from an unprivileged user gets `-32001` for operator methods.
- Fuzz the line framing and the JSON decoding.

## 9. Implementation notes and differences from this draft

What exists today:

- **Methods:** `lotus.hello`, `lotus.status`, `lotus.health`, `lotus.reload`, `lotus.stop`, `lotus.logs.reopen`, `lotus.config.inspect`, `lotus.site.list`, `lotus.cert.list`, `lotus.cache.status`, `lotus.cache.purge`. The others (`explain`, `upgrade`, `cert.renew`, `module.list`, `metrics`) arrive with the features they operate on. Metrics are served on their own listener instead (`docs/architecture/metrics.md`); `cert.renew` isn't built because renewal is automatic (about 30 days before expiry) and a manual renewal would need the certificate cache refreshed by hand. `lotus config explain` works offline from the configuration file, so it doesn't need the channel.
- **CLI:** `lotus status [--json]`, `lotus certs [--json] [--warn-days N]`, `lotus cache status [--zone NAME] [--json]`, `lotus cache purge [--zone NAME] [--host HOST] (--path PATH | --prefix PREFIX | --all)`, `lotus reload [--config FILE]`, `lotus stop [--now] [--timeout D]`, `lotus logs reopen`. Each takes `--control SOCKET` (or `LOTUS_CONTROL`).
- **Always on:** the channel is opened at start. Without a `control { socket }` setting it uses the platform default; if that location can't be used, the server logs a warning and runs without the channel. A socket path the administrator chose that can't be used stops the start.
- **Stop:** the server answers `lotus.stop` before it shuts down. Closing the channel lets a request in progress finish its reply (up to 5 s) and closes idle connections at once.

Differences, on purpose:

| Draft | Implementation | Why |
|---|---|---|
| Windows: named pipe `\\.\pipe\lotus-control` | Windows: a Unix-domain socket at `%ProgramFiles%\Lotus\control.sock` | Go's standard library supports Unix sockets on Windows 10 and later; a named pipe server needs a third-party package. Access control is the file ACL of the socket's directory. Checking those ACLs, and client identity, need the Windows security API and are not done yet. |
| Socket mode `0660`, group `lotus-admin` | Mode `0600` (owner only) | Group access belongs with roles. Until the peer's identity is checked, owner-only is the safe default. |
| Roles `operator` and `viewer` | Every client that can open the socket is `operator`; `control { viewer_group }` is refused at start | A viewer role needs the peer's identity (`SO_PEERCRED`, `getpeereid`, the Windows token). Linux reads it for the audit log; the role decision is not built. |
| Refuse to start if the directory is writable by others | Same, on Unix: group- or world-writable directories are refused unless sticky, and a directory owned by another user (not root) is refused. Missing directories are created with mode `0750`. | Windows: see above. |
| Default `/run/lotus/control.sock` | Same for root. Other users get `$XDG_RUNTIME_DIR/lotus/control.sock`, or `<tmp>/lotus-<uid>/control.sock`. macOS: `<tmp>/lotus/control.sock`. | A development server shouldn't need root. |
| Config path must be re-checked by the server | Done: `lotus.reload` takes only a path and the server loads and validates it itself; a successful reload from another path makes that file the current one. | |

Reload semantics: `lotus.reload` is synchronous and returns after the new configuration is published or rejected. A rejected configuration returns `-32020`, with the diagnostics for an invalid file or the reason for a change that needs a restart, and the running configuration stays. A second reload while one runs gets `-32021`. `SIGHUP` takes the same path (a busy reload is logged, not queued).
