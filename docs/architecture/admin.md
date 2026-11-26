# Web admin

The web admin is a sign-in page and a dashboard, served by LOTUS on a listener of its own (the idea of LiteSpeed's WebAdmin). It is optional and off unless the configuration has an `admin` block. It never replaces the control channel: it uses the same operations (`control.Backend`) as the `lotus` command, through `internal/admin`.

**Status (2026-11-26):** the plan is built: the `admin` block, accounts, sign-in, sessions, the dashboard (status, health, listeners, sites, certificates), Reload, the Sites page (add, edit, delete), the Cache, PHP and Server pages, the Logs viewer and the Configuration editor. Certificates that LOTUS obtains itself (`tls automatic`) have a Renew button on the Dashboard (admin role): it asks the CA for a new certificate now and loads it, checked against Pebble. A certificate issued less than a day ago is not renewed again, because CAs limit duplicate certificates (Let's Encrypt: 5 a week); `acme.MinRenewAge` holds that limit.

## Logs and Configuration (admin role only)

Both show raw data that can hold addresses, URLs and secrets, so every route needs the `admin` role, even to read.

- **Logs:** the end of a log file (the last 512 KiB; 100 to 1000 lines, newest last) with an optional text filter. The page picks a log by an id from the list the server gives (`service` for the Windows service's output file, `site.NAME.access` and `site.NAME.error` for site logs that are written to a file). It never sends a path, so no other file can be asked for. Lines are cut at 2,000 characters. On Linux the server's own output goes to the system journal, not to a file; use `journalctl -u lotus`.
- **Configuration:** edit `main.conf` or a `.conf` file directly in `sites.d` (no other path, drive or `..` is accepted). **Check** loads the whole configuration with the new text and shows the problems, without keeping anything. **Save and reload** does the same, then keeps the file (the old text stays next to it as `NAME.conf.bak`, which is not read as configuration), then reloads; if the server refuses the reload the old text is put back. A file with a mistake is never saved. Files are limited to 256 KiB of UTF-8 without NUL.
- A site file made by the Sites page shows a note: keep its first comment line and layout, or the Sites page lists it as written by hand.
- **Know what this allows:** someone who can save a configuration here can change what the server serves, reads and runs (for example a `php_backend` `command` runs a program as the service account). Admin accounts should be people who could already administer the machine. This is why the editor is for the `admin` role only, the sign-in is rate limited, and every save, refusal and undo is logged with the account and address.
- A change to the `admin { }` block itself is not applied by a reload; restart the service. Saving a mistake in it is still refused by the check.

## PHP versions (PHP page, Windows)

The panel can get PHP itself, so a machine needs no XAMPP: **Install** downloads an official build, **Settings** edits `php.ini`, **Make pool** creates the pool, **Remove** deletes a version (`internal/phpinstall`, `internal/admin/php_versions.go`).

- **Where it comes from.** The list is `releases.json` of windows.php.net (the newest version of each branch: 7.4 to the newest, NTS x64 builds, which php-cgi needs). Only `https://windows.php.net/` and, where it redirects its files, `https://downloads.php.net/` are contacted; a redirect anywhere else is refused. The version to install must be one the list offers.
- **How it installs.** Download (at most 150 MB) to a temporary file, check its SHA-256 against the list, unpack into `<version>.part` (every name is checked: no `..`, absolute path, drive letter, backslash or link; file count and size capped), check that `php-cgi.exe` is there and starts (`-v`), write `php.ini` from `php.ini-production` with an absolute `extension_dir` and the common extensions on, write `lotus-php.json`, rename to `<version>`. Any failure removes what was made. One install runs at a time; the page polls a job (`/v1/php-jobs/{id}`) while it runs, since a download takes a minute.
- **Where.** The `php` folder next to `main.conf` (Windows installer: `C:\Program Files\Lotus\php\8.3.37\`). The folder is fixed; no request names a path. `Remove` deletes only a folder that holds `lotus-php.json`, and is refused while a pool's program is inside it.
- **Visual C++ runtime.** PHP needs `vcruntime140.dll` and `vcruntime140_1.dll`. The panel looks for them first and, if they are missing, shows the message and the Microsoft download link instead of installing anything; the start check after unpacking catches other failures.
- **Settings and extensions.** Only a fixed list of `php.ini` settings is editable, shown in groups (limits, uploads, errors and logs, behaviour, sessions, opcode cache): sizes, numbers, On/Off switches, a few paths (error log, upload and session folders, CA files) and `disable_functions`; the page sends only the values that changed, and a path or list can be cleared with an empty value. Each value has its own pattern (a value with a newline or `;` is refused). Extensions are the `ext\php_*.dll` files the build has (opcache has its own setting). Editing rewrites only the line concerned (documentation lines in the file are left alone), keeps `php.ini.bak`, and then replaces the processes of the pools that use that version so they read it.
- **Pools and sites.** *Make pool* adds a pool named `php83` (`php83-37` if that name is taken) to the server settings file, 4 processes, `max_requests 500`, with the version's `php.ini`. The site form's PHP backend list shows `PHP 8.3.37 (php83)`, which is how a site picks its PHP version.
- **What this trusts.** The checksum comes from the same HTTPS site as the file: it protects against a damaged or swapped download, not against a compromised windows.php.net. The downloaded PHP then runs as the service account, like any pool. Everything needs the admin role; every install, removal and settings change is logged with the account and address.
- **PHP settings page** (`#phpsettings/<version>`, opened with the Settings button of a version): the settings form, the extensions and the ionCube Loader are on a page of their own, not under the version list.
- **ionCube Loader** (`internal/phpinstall/ioncube.go`; a card on the settings page; `GET|POST /v1/php-versions/{v}/ioncube`). The loader is a separate download from ionCube Ltd. **Download and enable** fetches the pack for the version from `downloads.ioncube.com` only (HTTPS, no redirect to another host, at most 40 MB) as a background job, takes the one loader file for this PHP out of it (Windows: `nonts_vc15` for PHP 7.2-7.4, `nonts_vc16` for 8.1-8.3, `nonts_vc17` for 8.4-8.5, nothing for 8.0 or 8.6; Linux: `ioncube_loader_lin_X.Y.so`, the `_ts` file for a thread-safe PHP, x86-64 or aarch64), and puts `zend_extension = "<path>"` at the top of `php.ini` (it must load before other Zend extensions). **PHP is then started with that php.ini** (`php-cgi -c ... -v`) and must report the loader; if it does not start or does not load it, `php.ini` is put back and the file removed. *Enable* and *Disable* switch the line; the pools that use the version restart. ionCube publishes no checksums, so unlike the PHP download the file is trusted on the strength of the HTTPS connection to their host, plus the start check. Checked for real on Windows (PHP 8.3.37, loader v15.5 loaded, then disabled) and in WSL Ubuntu (PHP 8.3.6).
- **Linux** (`phpinstall.LinuxManager`): PHP comes from the system's packages, not a zip.
  - *Found:* the `php-cgi` programs already installed (`/usr/bin/php-cgi*`, `/usr/lib/cgi-bin/php*`, `/usr/local/bin`, `/opt/remi/php*/root/usr/bin`) are listed with a **Use** button; each is asked for `-v` to get its version. Using one needs no root: the panel makes a folder `<config folder>/php/<version>/` with `lotus-php.json` (which names the program) and a **copy of the php.ini the system uses**, which is the file the settings editor changes, so the system's own file is never touched. The pool runs `php-cgi -c <that copy>`.
  - *Install:* the versions `apt-cache` (Debian, Ubuntu) or `dnf` (Fedora, RHEL family with Remi) offer, with **Install**. It works only when the LOTUS service runs as root; otherwise the button is off and the message gives the exact command. It runs `apt-get update` first (a stale index is the usual cause of "404 Not Found"), then `apt-get install -y --no-install-recommends` or `dnf install -y` with a fixed argument list (no shell): `phpX.Y-cgi` plus the extension packages the repository has (curl, mbstring, mysql, xml, zip, gd, intl, sqlite3, bcmath, opcache). Package names come from the package manager's own output and must match a plain-name pattern.
  - *Extensions* are packages on Linux, so they are shown (the modules PHP has loaded) but not switched here; install them with the package manager. *Remove* only stops using a version (its folder is deleted); the packages stay installed.
  - *Not covered:* repositories are never added (Ondrej's PPA, Remi): add them yourself, then the versions show up. Other package managers (yum, zypper, pacman) are not supported; a found php-cgi works everywhere.

## PHP pools and the HTTPS wizard

Both write one managed file, `sites.d/00-lotus-server.conf` (the `00` loads it first), made the same way as a site file: a marker line, a `# spec: {JSON}` line and text that must equal what the program renders from the JSON. A hand-edited server file is shown read-only and left alone. It is not listed on the Sites page; the Configuration editor can open it. Every save is checked as a whole configuration, written through a temporary file, loaded, and put back if refused, like a site change.

- **PHP pools (PHP page).** Add, edit and delete a `php_backend` that LOTUS starts php-cgi processes for: name, program (a full path such as `C:/xampp/php/php-cgi.exe`, or a bare name found in PATH), processes (1 to 64), replace a process after N requests (0 = never), timeout (1 to 600 s), optional php.ini path. A pool a site uses can't be deleted (the whole-configuration check says which site). Pools written in `main.conf` are listed with their live state but changed in the Configuration editor. **Restart** (`POST /v1/php/{name}/restart`) replaces every process of a running pool: an idle process is stopped and started again at once, a busy one finishes its request first (`phpproc.Pool.RecycleAll`); requests wait for a free process meanwhile, up to the pool timeout. A changed pool is replaced by the reload itself.
- **HTTPS wizard (Server page).** Contact e-mail, HTTPS address (default `0.0.0.0:443`), an optional plain-HTTP listener on `0.0.0.0:80` (for the HTTP-01 check) and "use the test certificate authority first". It writes the listeners `lotus-https` / `lotus-http` and `tls { acme { email, ca } }`. **Listeners and ACME settings cannot change while the server runs**, so the reload says "need a restart": the wizard then keeps the file and tells the person to restart the service (the admin has no restart; see Server). Any other refusal puts the file back. Remove undoes it the same way. Not offered when `main.conf` already has an ACME block.
- **A site with TLS off is attached to every listener by default**, and the configuration refuses a TLS-off site on an HTTPS listener. So before (or after) the wizard, limit such a site to the plain listener (Sites > Advanced > Listeners, for example `http`), or set its TLS to Automatic. The refusal names the site.
- The wizard cannot check DNS or the firewall; the page lists what is still needed (DNS pointing here, ports 80 and 443 open, TLS Automatic on the sites).

## Earlier versions (Configuration page, admin role)

Every change made through the Sites page or the Configuration editor keeps the text the file had before it, in `history/<file>/YYYYMMDD-HHMMSS-<account>.conf` next to the main configuration file (a site file `sites.d/x.conf` is kept in `history/sites.d__x.conf/`). The folder is outside `sites.d`, so no include reads a version as configuration. The newest 20 per file are kept; an unchanged text is not kept twice; a change that is refused or undone keeps nothing. The page lists them (time, account, size) and **Load into editor** puts one in the editor; the normal Check and Save and reload then apply, so a restored version is checked as a whole like any edit. Names and ids are checked against fixed patterns, so no other path can be asked for. Changes made with the `lotus site` command or by hand are not recorded. History is a convenience for undoing mistakes, not an audit log (the audit lines are in the server log).

## Users (admin role)

The Users page lists the accounts of the users file and adds, changes (role and/or password) and deletes them; the same file `lotus admin passwd` writes. Rules, all enforced on the server: a password has at least 10 characters; the last admin can't be deleted or demoted; you can't delete your own account or change your own role (another admin does that); changing your own password needs the current one; changing another account's password needs no old password. A changed or deleted account's sessions end at once (your own session stays when you change your own password), because a session keeps the role it was opened with. Every change is written to the server log with the account and address.

## Traffic (live statistics, any signed-in account)

Like the real-time statistics of other servers' admin panels. The page reads `GET /v1/traffic` every 3 seconds while it is open and the browser tab is visible (nothing is polled otherwise) and keeps the last 60 readings (3 minutes) in the browser. The server sends totals only, read from the atomic counters the metrics already keep (`Server.Traffic`, `internal/server/traffic.go`), so nothing is added to the request path; the page works out the rates from two readings. It shows requests per second, data sent per second, open connections against the limit, the share of errors, refused connections and dropped requests, three sparklines (requests, data, errors; drawn as inline SVG, no chart library) and a table by site (requests/s, data/s, 2xx, 3xx, 4xx, 5xx, average time, rate limited). Totals count from the start of the server; a reload does not reset them. Requests that no site answered are shown as "(no site matched)".

## New site: the web directory

For a new site the Web directory box is filled from the name as you type it: the default base plus the name, `C:/www/newsite.com` on Windows (`/var/www/public_html/newsite.com` on Linux). The box follows the name until you change it yourself; an existing site shows its own directory. "Create the web directory with a sample page" is on by default.

## Cache, PHP and Server pages

- **Cache:** a row per zone (entries, size against the budget, hits, misses, stale, evicted, purged) and a purge form: one path, everything under a path, or the whole zone, optionally for one host. Purging the whole zone asks for a confirmation. The work is done by the same functions as `lotus cache purge`.
- **PHP:** for each php-cgi pool that LOTUS starts itself (`php_backend` with `command`): the command, the wanted number of processes, how many run now, and how often one was restarted, with a Restart button; pools can be added, edited and deleted (see "PHP pools and the HTTPS wizard"). A `fastcgi_pass` backend is outside LOTUS and not listed.
- **Server:** Reload configuration and Reopen log files. There is deliberately no Stop or Restart: the admin is part of the server, so a stop would end the page that asked for it and could not be undone from there. Stop and start the service from the operating system (Windows: the Start Menu entries or `Stop-Service LOTUS`; Linux: `systemctl`), or `lotus stop` from a shell.

## Sites

The Sites page works on the files of the `sites.d` folder next to the main configuration file (`internal/sitefile`, the same code as `lotus site add|list|remove`). A site has a name, aliases, a web directory, index files and TLS `off` or `automatic`, plus a **type** and the settings of the Advanced section.

**Types (templates).** The type decides the routes; they are made from fixed fields, never from typed text:

| Type | What it writes |
|---|---|
| Static files | the site only |
| PHP application | `.php` files run through the chosen `php_backend`; other URLs that are not files go to `/index.php` (front controller); PHP never runs in `/uploads/` |
| WordPress | as PHP, with `/wp-content/uploads/` never executed |
| Laravel | as PHP, serving the `public` folder inside the web directory |
| Symfony, LPHP | as Laravel: the site root is the project folder, the web directory is its `public` folder |
| Drupal | as WordPress, with `/sites/default/files/` never executed; the web directory holds Drupal's `index.php` (no sample page is written) |
| Joomla | as WordPress, with `/images/` never executed; the web directory holds Joomla's `index.php` (no sample page is written) |
| Reverse proxy | an `upstream "site-NAME"` with one backend (`http(s)://host:port`) and a route `/` that proxies to it; no web directory, no `root` line |

**Default PHP.** The first pool made is the default (`default_php` in the server file's spec); the PHP page can change it (`PUT /v1/php-default`), and the Add site form starts with it. Deleting the default pool makes the first remaining pool the default. A restart of a pool (PHP settings saved, ionCube changed, the Restart button) waits up to 10 seconds until every process runs again, so "restarted" is true when it is shown; a process busy with a longer request is replaced when that request ends.

**Advanced settings** (all optional): rate limit (requests per second, minute or hour per client, with burst), compression on/off (default: the server's), security headers (basic: nosniff, frame, referrer), symbolic links, dotfiles, `.htaccess`, error pages for 403, 404 and 500, access and error log files. Each is an enum or a strict pattern, and the whole configuration is checked before anything is kept.

**File format.** A file has the marker line, then `# spec: {JSON}` with everything the form holds, then the rendered text. It is editable only if its whole text equals what the program renders from that JSON; any hand edit (even of the JSON) makes it "written by hand" and read-only here. Files made before this format (marker and fixed lines, no spec line) are still read as static sites and upgraded on the next save. The PHP backend list in the form is the `php_backend` names of the current configuration.

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
| `GET /v1/site-files` | any | `{entries: [{name, aliases, root, index, tls, template, php_backend, proxy_url, rate_requests, rate_per, rate_burst, compression, security_headers, symlinks, dotfiles, htaccess, error_pages, access_log, error_log, file, editable}], default_root, templates, php_backends, hint}` |
| `POST /v1/site-files` `{name, aliases, root, index, tls, template, …settings, create_root}` | admin | `{ok, generation}`; 422 for a bad value or an invalid configuration (with `diagnostics`), 409 if the name exists |
| `PUT /v1/site-files/{name}` `{aliases, root, index, tls}` | admin | as above; 404 if missing, 409 if the file was changed by hand |
| `DELETE /v1/site-files/{name}` | admin | `{ok, generation}`; the web directory is not touched |
| `POST /v1/certs/renew` `{domain}` | admin | `{ok}` after the CA has issued the certificate (up to a minute); 404 for a name without an automatic certificate, 422 when the current one is under a day old |
| `GET /v1/traffic` | any | `{time_ms, uptime_seconds, connections, connection_limit, rejected, dropped, classes, sites: [{name, requests, class[], bytes, rate_limited, latency_seconds}], unrouted}`: running totals since the start |
| `GET /v1/php-versions` | any | `{supported, runtime_ok, runtime_message, feed_error, installed: [{version, dir, exe, ini, installed_at, pools}], available: [{branch, version, size, installed}], job}` |
| `POST /v1/php-versions` `{version}` | admin | `202 {job}`; 422 for a bad version, a missing runtime or a system without support; 409 while another install runs |
| `GET /v1/php-jobs/{id}` | any | `{id, version, state: running|done|failed, percent, message}` |
| `DELETE /v1/php-versions/{version}` | admin | `{ok}`; 409 while a pool uses it, 404 if not installed |
| `GET /v1/php-versions/{version}/ini` | any | `{settings, enabled, available, setting_info}` |
| `PUT /v1/php-versions/{version}/ini` `{settings, extensions}` | admin | `{ok, restarted}`; 422 for a refused value or unknown extension |
| `POST /v1/php-versions/{version}/pool` `{name?, processes?}` | admin | makes a pool for the version (as `PUT /v1/php-pools`); 409 if the name is taken |
| `GET /v1/php-versions/{version}/ioncube` | any | `{available, message, downloaded, enabled}` |
| `POST /v1/php-versions/{version}/ioncube` `{action: install|enable|disable}` | admin | `install`: `202 {job}`; the others `{ok, restarted}`; 422 with the reason when the loader can't be enabled |
| `GET /v1/php-pools` | any | `{pools: [{name, command, processes, max_requests, php_ini, timeout}], editable, can_restart}`: the pools of the server file |
| `PUT /v1/php-pools/{name}` `{command, processes, max_requests, php_ini, timeout}` | admin | add or replace; `{ok, generation}`; 422 for a bad value or an invalid configuration; 409 if the file was changed by hand |
| `DELETE /v1/php-pools/{name}` | admin | `{ok, generation}`; 404 if it is not in the server file; 422 if a site uses it |
| `POST /v1/php/{name}/restart` | admin | `{ok}`; 404 if no such pool runs |
| `GET /v1/https` | admin | `{spec, editable, listeners, acme, acme_email, files_valid, staging_ca}` |
| `PUT /v1/https` `{email, https, http, staging}` | admin | `{ok, generation}` or `{ok, restart_required, message}`; 422 as above |
| `DELETE /v1/https` | admin | as above; 404 if the wizard has not set it up |
| `GET /v1/users` | admin | `[{name, role, self}]` (never a hash) |
| `POST /v1/users` `{name, role, password}` | admin | `{ok}`; 422 for a bad name, role or short password, 409 if the name exists |
| `PUT /v1/users/{name}` `{role?, password?, current_password?}` | admin | `{ok, sessions_ended}`; 422 for the last-admin and own-role rules, 403 for a wrong current password |
| `DELETE /v1/users/{name}` | admin | `{ok}`; 422 for yourself or the last admin, 404 if missing |
| `GET /v1/config/history?name=` | admin | `{name, versions: [{id, time, who, size}], keep}` |
| `GET /v1/config/version?name=&id=` | admin | `{name, id, content}`; 404 for an id that was not made by the program |
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
