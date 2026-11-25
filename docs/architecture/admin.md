# Web admin

The web admin is a sign-in page and a dashboard, served by LOTUS on a listener of its own (the idea of LiteSpeed's WebAdmin). It is optional and off unless the configuration has an `admin` block. It never replaces the control channel: it uses the same operations (`control.Backend`) as the `lotus` command, through `internal/admin`.

**Status (2026-11-26):** step 1 of the plan is built: the `admin` block, accounts, sign-in, sessions, the dashboard (status, health, listeners, sites, certificates) and Reload. Not built yet: adding and editing sites, certificate renewal, PHP, cache, logs, the configuration editor (next steps).

## Configuration

```text
server {
    admin {
        address "127.0.0.1:7080"
        # users_file "admin-users.txt"   # next to the main configuration file by default
        # session_ttl 8h
        # certificate "/etc/lotus/admin.pem"
        # certificate_key "/etc/lotus/admin.key"
        # allow [ "10.0.0.0/8" ]
    }
}
```

- A listener that is not on a loopback address is refused at validation unless `certificate` and `certificate_key` are set (the two go together).
- A change to the `admin` block is not applied by a reload; restart the server (the reload says so).
- The directive pages are in `docs/configuration/directives/` (generated).

## Accounts

```text
lotus admin passwd NAME [--role admin|viewer] [--config FILE | --users FILE]
lotus admin users
```

The password is asked twice without echo; `--password-stdin` and `--password-file FILE` are for scripts and the installer. At least 10 characters. Passwords are stored as argon2id hashes in the users file (`name:role:hash`, one per line); the file is read at every sign-in, so a change works at once. `admin` may change things (Reload today), `viewer` only looks.

## Security

- Sessions: a random 256-bit id in a cookie that is `HttpOnly`, `SameSite=Strict` (and `Secure` when the listener is HTTPS), kept in memory, ended by sign-out, expiry (`session_ttl`) or a restart. The cookie is `Secure` only with HTTPS so that the loopback `http://127.0.0.1:7080/` works.
- Every request that changes something needs the session's CSRF token in `X-CSRF-Token` (given at sign-in) and the `admin` role.
- Five wrong passwords from one address lock that address out for ten minutes. A wrong name costs as much time as a wrong password.
- The page and script are embedded in the program, with no inline script or style and no outside resources: `Content-Security-Policy: default-src 'self'`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`. Text from the server is put into the page with `textContent` only.
- `allow` is checked against the connection's address (not `X-Forwarded-For`).
- Sign-ins, failures, sign-outs and reloads are written to the log with the address and account name. Passwords are never logged.
- On Windows the users file is not protected by file mode; the installer restricts it to SYSTEM and Administrators with `icacls`. On Linux it is written with mode `0600`.

## API (`/v1`, JSON)

| Request | Role | Result |
|---|---|---|
| `POST /v1/login` `{name, password}` | none | `{user, role, csrf}` and the cookie |
| `POST /v1/logout` | any | `{ok}` |
| `GET /v1/session` | any | `{user, role, csrf}` |
| `GET /v1/status`, `/v1/health`, `/v1/sites`, `/v1/certs` | any | as `lotus.status`, `lotus.health`, `lotus.site.list`, `lotus.cert.list` |
| `POST /v1/reload` | admin | `{ok, generation, diagnostics}`; 422 with the diagnostics for an invalid configuration |

## Not covered by tests

The unit tests and a run of the real program (sign in, status, reload, CSRF refusal) are done. The page itself has not been opened in a browser by anyone yet, and HTTPS mode has no test run.
