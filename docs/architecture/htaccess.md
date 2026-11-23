# `.htaccess` support

Status: a subset aimed at what PHP applications ship (WordPress, Laravel,
Drupal, Joomla, Symfony). Not built yet: HTTP Basic authentication, `Expires*`,
`php_value` / `php_flag`, `AddType` / `AddHandler`, `RewriteOptions Inherit`,
`<Limit>`, `SetEnvIf`. Check a file before you enable it:

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
| `headers` | `Header set\|setifempty\|add\|append\|merge\|unset` (with or without `always`) |

Containers: `<IfModule>` (true for `rewrite`, `alias`, `dir`, `headers`,
`authz_core`, `authz_host`, `access_compat`; false for every other module, with a
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
