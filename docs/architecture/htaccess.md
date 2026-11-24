# `.htaccess` support

Status: a subset aimed at what PHP applications ship (WordPress, Laravel,
Drupal, Joomla, Symfony). Not built yet: `<Limit>`/`<LimitExcept>`,
`RewriteOptions Inherit`, `SetEnvIf`, digest authentication, groups,
`ForceType`/`AddHandler` (refused on purpose). Check a file before you enable it:

```
lotus compat htaccess check /var/www/example/.htaccess
```

## Turning it on

Off by default, and a decision of the administrator, not of the site:

```
site "example.com" {
    root "/var/www/example"
    htaccess true
    htaccess_allow [ "rewrite", "redirect", "error_document", "directory_index", "options", "access", "headers" ]  # the default
    htaccess_unsupported "error"     # or "ignore"
    htaccess_recheck 2s
}
```

None of these can be set from a file a tenant includes into their own site. See
`configs/examples/htaccess.lotus`.

## What is supported

| Category | Directives |
|---|---|
| `rewrite` | `RewriteEngine`, `RewriteBase`, `RewriteCond`, `RewriteRule` |
| `redirect` | `Redirect` (also `permanent`, `temp`, `seeother`, `gone`, a status), `RedirectMatch`, `RedirectPermanent`, `RedirectTemp` |
| `error_document` | `ErrorDocument` with a path, a URL or `"text"` |
| `directory_index` | `DirectoryIndex` (file names, or `disabled`) |
| `options` | `-Indexes`, `-MultiViews`, `-ExecCGI`, `-Includes`, `None` (accepted); `+FollowSymLinks` and `+SymLinksIfOwnerMatch` are accepted and **have no effect**; `+Indexes`, `+MultiViews`, `+ExecCGI`, `+Includes`, `All` are refused |
| `access` | `Require all granted\|denied`, `Require ip`, `Require not ip` inside `<RequireAll>` / `<RequireNone>`, `<RequireAny>`; `Order`, `Allow from`, `Deny from` |
| `headers` | `Header set\|setifempty\|add\|append\|merge\|unset` (with or without `always`); `ExpiresActive`, `ExpiresDefault`, `ExpiresByType`; `AddDefaultCharset`; `AddType` |
| `php` | `php_value`, `php_flag` (PHP-FPM backends only, see below) |

`access` also covers HTTP Basic authentication: `AuthType Basic`, `AuthName`,
`AuthUserFile`, `AuthBasicProvider file`, with `Require valid-user` and
`Require user name...`.

Containers: `<IfModule>` (true for `rewrite`, `alias`, `dir`, `headers`,
`authz_core`, `authz_host`, `access_compat`, `expires`, `mime`, `authn_core`, `authn_file`, `auth_basic`, `authz_user`; false for every other module, with a
note in the log), `<Files>` and `<FilesMatch>` (for `Require`/`Order`/`Allow`/`Deny` and `Header`).

Ignored with a note, because they have no meaning here: `ServerSignature`,
`IndexIgnore`, `IndexOptions`.

## How a file is found and applied

- For a request, the files from the document root down to the requested
  directory are used. Never one above the root, never one reached through a
  symlink the site's `symlinks` policy refuses.
- A file is applied where the request would be served from the file system: to
  static files and PHP scripts, not to a route that is a fixed `return`, and not
  while an error page is being served.
- Parsed files are remembered. A file is read again when its modification time
  or size changed, but the file system is only looked at every
  `htaccess_recheck` (default 2 s; `0s` means every request), so a request does
  not stat every directory.
- Per request the order is: **access** (403), then **rewrite**, then
  **Redirect/RedirectMatch**. A rewrite to a new URL routes the request again
  from the start, so a route, `try_files` and the `.htaccess` of the new
  directory all apply to it. At most 10 such internal redirects (`LOTUS-RT-0001`).
- `.htaccess`, `.htpasswd` and every other name starting with `.ht` is never
  served, whatever the `dotfiles` setting says, and a request for one is a 403 even
  when a catch-all rewrite rule would otherwise take it for a missing page.

## Rewrite rules

They follow `mod_rewrite` in per-directory context:

- The pattern is matched against the URL path **below the directory of the file**,
  without a leading slash (`^index\.php$` in the root file, `^x$` for `/blog/x`
  in `/blog/.htaccess`).
- A substitution starting with `/` is a URL path. Any other relative substitution
  is added to `RewriteBase`, which defaults to the directory's URL.
- A substitution with a scheme (`https://...`) is a redirect.
- The query string is kept unless the substitution has its own (`?a=b` replaces
  it; `QSA` appends to it; `QSD` discards it).
- When a rule changes the URL the request is routed again, so the rules see the
  new URL too. That is why the standard files guard themselves
  (`RewriteRule ^index\.php$ - [L]`, `RewriteCond %{REQUEST_FILENAME} !-f`).
- `RewriteCond`: `%{VAR}`, `%{HTTP:Name}`, `%{ENV:name}`, `%N` captures of the
  last matching condition, `$N` captures of the rule; patterns, `!pattern`,
  `-f`, `-d`, `-s`, `=`, `<`, `>`, `<=`, `>=`; flags `NC`, `OR`.
- `RewriteRule` flags: `L`, `END`, `R[=code]`, `F`, `G`, `NC`, `NE`, `QSA`, `QSD`,
  `PT`, `C`, `S=n`, `E=name:value`. Others (`P` proxy, `H=`/`T=` handlers,
  `CO=` cookies, `B`, `N`) are refused.
- Variables: `REQUEST_URI`, `REQUEST_FILENAME`, `SCRIPT_FILENAME`,
  `DOCUMENT_ROOT`, `QUERY_STRING`, `REQUEST_METHOD`, `REQUEST_SCHEME`, `HTTPS`,
  `REMOTE_ADDR`, `SERVER_NAME`, `SERVER_PORT`, `THE_REQUEST`, `HTTP_HOST`,
  `HTTP_USER_AGENT`, `HTTP_REFERER`, `HTTP_COOKIE`, `HTTP_ACCEPT*`, `HTTP:name`,
  `ENV:name`. Any other variable is refused (silently expanding it to nothing
  would change what the rule means).
- `-f`, `-d` and `-s` only look at files **below the document root**, through the
  same resolver as static files (hidden files and symlink policy apply), and
  report "does not exist" for anything else.
- A rule in a child directory **replaces** the parent's rules (as in Apache
  without `RewriteOptions Inherit`); a child file with no rewrite directives
  leaves the parent's in force; `RewriteEngine Off` in a child turns them off.
- `E=name:value` variables are given to PHP as FastCGI parameters. They never
  replace a variable the server sets (`SCRIPT_FILENAME`, `REMOTE_ADDR`, ...).

### Differences from Apache you will notice

- **Patterns use Go's RE2 syntax**, not PCRE: no lookahead (`(?!...)`),
  look-behind or back-references. A file that uses them is reported with the
  line, not guessed at. Drupal's own `.htaccess` has one such pattern (in a
  `<FilesMatch>` that protects dotfiles; LOTUS's `dotfiles deny` already does that,
  so remove the `(?!well-known)` part).
- **mod_rewrite runs before mod_alias**, as in Apache. A `Redirect` written below
  a catch-all `RewriteRule` (WordPress's) never fires, because the rewrite already
  sent the request to `/index.php`. Write such a redirect as
  `RewriteRule ^old$ /new [R=301,L]` above the catch-all.
- **`Header`** is applied to every response, not only successful ones (`always`
  is accepted and means the same), and only to responses the file handler or
  PHP produces: not to error pages. It never changes a header set by the site's
  `security_headers`.
- **`<Files>`** matches the name in the request URL. A directory request that
  is served by an index file does not match a `<Files "index.php">` block.
- **`Redirect`** keeps the query string; **`RedirectMatch`** does not (as in Apache).
- **`Require ip`** compares the client address after `trusted_proxies`.
- A relative `Location` (`/new`) is sent where Apache would send a full URL.

## What happens to what is not supported

Every directive that LOTUS can't apply, or that the site doesn't allow, is
recorded with its file and line when the file is read, and logged once per
version of the file.

- With `htaccess_unsupported "error"` (the default) a request for a directory
  whose `.htaccess` has such a directive gets a **500**, and the client is told
  nothing about the file. Apache does the same for a directive it doesn't know.
- With `"ignore"` the directive is skipped with a warning. **Directives that grant
  or restrict access are never skipped:** `Require`/`Order`/`Allow`/`Deny` forms
  that can't be honoured (`Require valid-user`, `Require host`, host names in
  `Allow from`, ...), `Auth*`, `Satisfy`, `<Limit>`, a `<Files>` block that can't
  be compiled, and any line that doesn't parse. They always fail the request.

## Expires, charset and types

- `ExpiresActive On` switches `ExpiresDefault` and `ExpiresByType` on (they do
  nothing without it). Lifetimes are `access plus 1 month 2 days` (also `now
  plus ...`) or `A2592000` seconds; a month is 30 days and a year 365. Lifetimes
  counted from the file's modification time (`modification plus ...`, `M...`)
  are refused.
- A lifetime becomes `Cache-Control: max-age=N` and `Expires`, on 200, 206 and
  304 responses only. `ExpiresByType` takes an exact type or `image/*`. A script
  that sent its own `Expires` or `max-age` keeps it.
- `AddDefaultCharset UTF-8` adds the charset to `text/html` and `text/plain`
  responses that have none. `Off` in a deeper directory turns it off.
- `AddType font/woff2 .woff2` sets the Content-Type of static files by
  extension. It never applies to PHP responses, and a type that names a
  handler (`application/x-httpd-php`, `text/x-server-parsed-html`) is refused:
  which files run as code is decided by the server's `php` route.
- None of these can change a header the site's `security_headers` set.

## PHP settings

`php_value name value` and `php_flag name on|off` become the `PHP_VALUE`
FastCGI parameter, as single-quoted INI strings: no `${VAR}` expansion, no
constants or operators (`error_reporting E_ALL` doesn't work, as with Apache; use
the number). Deeper files override shallower ones, setting by setting.

- **PHP-FPM only.** `php-cgi` (what LOTUS runs for a `command` backend) ignores
  `PHP_VALUE`; checked against PHP 8 on Windows. With a managed pool the settings
  can't be applied, so a request to a directory that has them gets a **500**
  (logged), or with `htaccess_unsupported "ignore"` they are skipped with a
  warning. Put per-site settings in the pool's `php_ini` instead.
- A file can't change settings that belong to the administrator: `open_basedir`,
  `disable_functions`, `disable_classes`, `auto_prepend_file`,
  `auto_append_file`, `allow_url_include`, `allow_url_fopen`, `extension*`,
  `error_log`, `include_path`, the session and temp directories, and similar.
  Such a line is unsupported (see below). `php_admin_value` and `php_admin_flag`
  are always refused.
- `E=PHP_VALUE:...` in a `RewriteRule` doesn't work either: variables named
  `PHP_VALUE`, `PHP_ADMIN_VALUE` and `FCGI_*` are never passed from a file. (They
  would have bypassed both rules above.)
- Not tested against a real PHP-FPM yet: the INI quoting was checked with PHP's
  own `parse_ini_string`.

## HTTP Basic authentication

```
AuthType Basic
AuthName "Staff only"
AuthUserFile .htpasswd
Require valid-user          # or: Require user alice bob
```

- Password hashes: bcrypt (`$2y$`, `$2a$`, `$2b$`; cost up to 12 is checked,
  higher is refused so a hash can't be used to burn CPU), Apache MD5 (`$apr1$`,
  the `htpasswd` default) and `{SHA}`. Plain text and `crypt(3)` entries never
  match.
- `AuthUserFile` is an absolute path or relative to the directory of the
  `.htaccess` that names it, and the file **must be inside the site's document
  root** (symlinks resolved). `.htpasswd` is never served, so keeping it there is
  safe. A missing or outside file locks everybody out (and is logged); it never
  opens the directory.
- Without credentials, or with wrong ones, the answer is **401** with
  `WWW-Authenticate: Basic realm="..."`. A valid user who isn't allowed is a
  **403**. Combined with `Require ip` the usual rules apply: any of the top-level
  lines, or all of them inside `<RequireAll>`. `Require valid-user` without
  `AuthType Basic` and `AuthUserFile` is a 500, never "open".
- The password file is re-read when it changes (checked every `htaccess_recheck`).
  At most four password checks run at once.
- A PHP script gets `REMOTE_USER` and `AUTH_TYPE`.
- Not supported: Digest, `AuthGroupFile`, `Require group`, `Require not user`,
  LDAP, `Satisfy`.
- Wrong passwords are limited per client address: after `htaccess_auth_failures`
  (default 10) wrong attempts within `htaccess_auth_lockout` (default 5 minutes)
  the address gets 429 until the lock-out ends, and its passwords aren't checked.
  See `docs/architecture/limits.md`.

## Limits and safety

- A file is data, never code, and applies only to its own directory tree.
- Files over 256 KiB, more than 5 000 directives, nesting deeper than 8, and
  lines over 16 KiB are refused. Regular expressions run in linear time (RE2).
- A `.htaccess` can't loosen the site: not the `symlinks` and `dotfiles` policy
  (`Options +FollowSymLinks` has no effect), not the `security_headers`, not the
  PHP backend or the environment PHP is started with.
- A `.htaccess` that is a symlink leading outside the document root, or refused by
  the `symlinks` policy, is not read.
- The parser, compiler and evaluator are fuzzed (`FuzzHtaccess`).
