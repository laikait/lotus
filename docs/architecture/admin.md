# Web admin

The web admin is a sign-in page and a dashboard, served by LOTUS on a listener of its own (the idea of LiteSpeed's WebAdmin). It is optional and off unless the configuration has an `admin` block. It never replaces the control channel: it uses the same operations (`control.Backend`) as the `lotus` command, through `internal/admin`.

**Status (2026-11-26):** steps 1 and 2 of the plan are built: the `admin` block, accounts, sign-in, sessions, the dashboard (status, health, listeners, sites, certificates), Reload, and the Sites page (add, edit, delete). Not built yet: certificate renewal, PHP, cache, logs, the configuration editor (next steps).

## Sites

The Sites page works on the files of the `sites.d` folder next to the main configuration file (`internal/sitefile`, the same code as `lotus site add|list|remove`). A site has a name, aliases, a web directory, index files and TLS `off` or `automatic`.

- A file made here starts with a marker line and has a fixed layout. Only such a file can be edited or deleted in the admin. A file changed by hand (anything beyond that layout, or without the marker) is listed as "written by hand" and never rewritten. Sites in `main.conf` show on the Dashboard only.
- Every change is written to a temporary file, renamed into place, and the **whole configuration is checked**. If it is not valid, the old file is put back and the problems (file, line, message, hint) are shown in the form.
- A valid change is then loaded with a reload. If the server refuses the reload (for example a port it cannot bind), the file is put back too, so the files always match what runs.
- Values are checked strictly because they are written into a file: a host-name pattern for the name and aliases, an absolute web directory without quotes, backslashes, `$`, braces, control characters or `..`, plain file names for the index.
- This is a deliberate, narrow exception to "the admin API never writes source configuration files" (spec §9.2): only `sites.d/*.conf`, only through this package, never `main.conf`.

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
| `GET /v1/site-files` | any | `{entries: [{name, aliases, root, index, tls, file, editable}], default_root, hint}` |
| `POST /v1/site-files` `{name, aliases, root, index, tls, create_root}` | admin | `{ok, generation}`; 422 for a bad value or an invalid configuration (with `diagnostics`), 409 if the name exists |
| `PUT /v1/site-files/{name}` `{aliases, root, index, tls}` | admin | as above; 404 if missing, 409 if the file was changed by hand |
| `DELETE /v1/site-files/{name}` | admin | `{ok, generation}`; the web directory is not touched |

## Not covered by tests

The unit tests and a run of the real program (sign in, status, reload, CSRF refusal, adding a site and seeing it served, a refused edit, deleting it) are done. The page itself has not been opened in a browser by anyone yet, and HTTPS mode has no test run.
