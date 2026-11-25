# Web admin

The web admin is a sign-in page and a dashboard, served by LOTUS on a listener of its own (the idea of LiteSpeed's WebAdmin). It is optional and off unless the configuration has an `admin` block. It never replaces the control channel: it uses the same operations (`control.Backend`) as the `lotus` command, through `internal/admin`.

**Status (2026-11-26):** the plan is built: the `admin` block, accounts, sign-in, sessions, the dashboard (status, health, listeners, sites, certificates), Reload, the Sites page (add, edit, delete), the Cache, PHP and Server pages, the Logs viewer and the Configuration editor. Not built: certificate renewal (certmagic's renew call does not refresh the certificate in memory, and it needs a real CA to test).

## Logs and Configuration (admin role only)

Both show raw data that can hold addresses, URLs and secrets, so every route needs the `admin` role, even to read.

- **Logs:** the end of a log file (the last 512 KiB; 100 to 1000 lines, newest last) with an optional text filter. The page picks a log by an id from the list the server gives (`service` for the Windows service's output file, `site.NAME.access` and `site.NAME.error` for site logs that are written to a file). It never sends a path, so no other file can be asked for. Lines are cut at 2,000 characters. On Linux the server's own output goes to the system journal, not to a file; use `journalctl -u lotus`.
- **Configuration:** edit `main.conf` or a `.conf` file directly in `sites.d` (no other path, drive or `..` is accepted). **Check** loads the whole configuration with the new text and shows the problems, without keeping anything. **Save and reload** does the same, then keeps the file (the old text stays next to it as `NAME.conf.bak`, which is not read as configuration), then reloads; if the server refuses the reload the old text is put back. A file with a mistake is never saved. Files are limited to 256 KiB of UTF-8 without NUL.
- A site file made by the Sites page shows a note: keep its first comment line and layout, or the Sites page lists it as written by hand.
- **Know what this allows:** someone who can save a configuration here can change what the server serves, reads and runs (for example a `php_backend` `command` runs a program as the service account). Admin accounts should be people who could already administer the machine. This is why the editor is for the `admin` role only, the sign-in is rate limited, and every save, refusal and undo is logged with the account and address.
- A change to the `admin { }` block itself is not applied by a reload; restart the service. Saving a mistake in it is still refused by the check.

## Cache, PHP and Server pages

- **Cache:** a row per zone (entries, size against the budget, hits, misses, stale, evicted, purged) and a purge form: one path, everything under a path, or the whole zone, optionally for one host. Purging the whole zone asks for a confirmation. The work is done by the same functions as `lotus cache purge`.
- **PHP:** read-only. For each php-cgi pool that LOTUS starts itself (`php_backend` with `command`): the command, the wanted number of processes, how many run now, and how often one was restarted. A `fastcgi_pass` backend is outside LOTUS and not listed.
- **Server:** Reload configuration and Reopen log files. There is deliberately no Stop or Restart: the admin is part of the server, so a stop would end the page that asked for it and could not be undone from there. Stop and start the service from the operating system (Windows: the Start Menu entries or `Stop-Service LOTUS`; Linux: `systemctl`), or `lotus stop` from a shell.

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
| `GET /v1/cache` | any | the cache zones, as `lotus.cache.status` |
| `POST /v1/cache/purge` `{zone, host, path, prefix, all}` | admin | `{ok, purged}`; 422 unless exactly one of path, prefix, all; 404 for an unknown zone |
| `GET /v1/php` | any | `[{name, command, processes, alive, restarts}]` |
| `POST /v1/logs/reopen` | admin | `{ok, reopened}` |
| `GET /v1/logs` | admin | `[{id, name}]` |
| `GET /v1/logs/{id}?lines=200&filter=text` | admin | `{lines, size}` (404 for an id that is not listed) |
| `GET /v1/config/files` | admin | `{files: [{name, size, managed}], max_size}` |
| `GET /v1/config/file?name=main.conf` | admin | `{name, content}` |
| `POST /v1/config/validate` `{name, content}` | admin | `{ok, diagnostics}` or 422 with the problems; nothing is kept |
| `PUT /v1/config/file` `{name, content}` | admin | `{ok, generation}`, or 422 with the problems (nothing saved) |

## Not covered by tests

The unit tests and a run of the real program (sign in, status, reload, CSRF refusal, adding a site and seeing it served, a refused edit, deleting it) are done. The page itself has not been opened in a browser by anyone yet, and HTTPS mode has no test run.
