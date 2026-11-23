# Local Control Channel Protocol

**Protocol version:** `lotus.control.v1`
**Status:** Phase 0 draft for review
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
| `lotus.cert.list` | viewer | — | `[ { domains, issuer, not_after, managed, status } ]` |
| `lotus.cert.renew` | operator | `{ "domain", "force": bool }` | `{ "scheduled": true }` |
| `lotus.cache.status` | viewer | `{ "zone"? }` | sizes, entries, hit ratio |
| `lotus.cache.purge` | operator | `{ "zone", "key"? , "prefix"? , "tag"? , "all": bool }` | `{ "purged": n }`. `all` must be explicit. |
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
