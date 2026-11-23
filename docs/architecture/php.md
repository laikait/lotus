# PHP through FastCGI

Status: implemented for FastCGI backends reached over TCP or a Unix socket
(PHP-FPM, or `php-cgi -b`), and for `php-cgi` processes that LOTUS starts and
supervises itself (the way to run PHP on Windows). Not built yet: per-site pools
and OS users, `max_conns` and `fastcgi_param` directives, PHP extensions other
than `.php`.

## Configuration

```
server {
    php_backend "app" {
        fastcgi_pass "unix:/run/php/php-fpm.sock"   # or "127.0.0.1:9000"
        timeout 60s
    }
    site "example.com" {
        route ~ '\.php(/|$)' { php "app" }
        route ^~ "/uploads/" { php_deny true }
        route "/" { try_files [ "{uri}", "{uri}/", "/index.php?{query}" ] }
    }
}
```

A backend is either `fastcgi_pass` (a PHP server that runs on its own) or
`command` (LOTUS runs it, see "Managed processes" below), never both.

`php_backend` is server-level and the `php` action is tenant-forbidden: which
PHP, and so which pool and OS user, runs a site is the administrator's decision.
See `configs/examples/php.lotus`.

## Which file runs

1. The first path segment that ends in `.php` (any letter case) ends the script's
   path; the rest is `PATH_INFO`. `/blog/index.php/a/b` runs `/blog/index.php`
   with `PATH_INFO=/a/b`. Later `.php` segments are just path info.
2. If the path has no such segment, a directory request may lead to an index file
   (`index` directive) that is a `.php` file; that one runs.
3. The script must exist below the document root and pass the same resolver as
   static files (traversal, hidden files, symlink policy, Windows hazards). If it
   doesn't, the answer is 404 (403 for a policy refusal) and PHP is **never
   asked**. In particular `/uploads/evil.jpg/x.php` names no file, so an uploaded
   image can't be run through a path trick.
4. Files that are not `.php` on a `php` route are served as static files.

## PHP source is never sent as text

- A route with `php_deny true` refuses `.php` files (403) instead of running them.
  Other files there are served normally.
- In a site where any route has `php`, a route without it refuses `.php` files
  (403) instead of serving their source. A directory whose index is a `.php`
  file is routed again as that file, so the route that runs PHP gets it (Nginx
  does the same with `index`).
- A site with no `php` action anywhere is a plain static site and serves every
  file as it is.
- Only `.php` counts. `.phtml`, `.phar`, `.inc` and friends are ordinary files to
  LOTUS; keep them out of the document root or deny them with a route.

## The CGI environment

`SCRIPT_FILENAME`, `SCRIPT_NAME`, `PATH_INFO`, `DOCUMENT_ROOT`, `QUERY_STRING`,
`REQUEST_URI` (the URL the client sent, not the one after rewrites or
`try_files`), `REQUEST_METHOD`, `SERVER_PROTOCOL`, `SERVER_NAME`, `SERVER_ADDR`,
`SERVER_PORT`, `REMOTE_ADDR` (the client address after `trusted_proxies`),
`REMOTE_PORT`, `HTTPS` (`on` over TLS), `REQUEST_SCHEME`, `CONTENT_TYPE`,
`CONTENT_LENGTH`, `REDIRECT_STATUS`, `HTTP_*` for request headers.

Headers that are **not** passed on: `Proxy` (`HTTP_PROXY` is read by many HTTP
client libraries as the proxy to use; "httpoxy") and any header whose name
contains an underscore (`X_Foo` and `X-Foo` would both become `HTTP_X_FOO`, so a
client could forge a header a proxy in front meant to set). A NUL byte in any
value is refused.

A request body of unknown length (chunked) is read completely first, in memory up
to 1 MiB and in a temporary file beyond that, because PHP needs `CONTENT_LENGTH`
up front. `max_request_body` still applies (413).

## The response

- `Status:` sets the status; `Location:` alone means 302. Several `Set-Cookie`
  headers each arrive.
- Dropped: hop-by-hop headers, `Server`, `X-Sendfile` and `X-Accel-*` (they are
  instructions to a front server that LOTUS does not follow).
- A header the site's `security_headers` already set is not overridden by PHP.
- Output is streamed: what the script flushes reaches the client at once.
- A script's own responses, 404 included, are never replaced by `error_page`
  (like Nginx without `fastcgi_intercept_errors`). Failures of the gateway are.

## Failures

| What happened | Client sees | Logged |
|---|---|---|
| Backend unreachable, crashed before output, bad CGI headers | 502 | the error, and PHP's stderr if any |
| Backend overloaded | 503 | |
| Backend did not answer within `timeout` | 504 | |
| Request body over the limit | 413 | |
| Client went away | nothing (499 in the access log) | |
| Backend died after the status was sent | the connection is cut, so the client can't mistake a truncated page for a whole one | |

The client is never shown PHP's error output or the backend's address. `timeout`
starts when the request, body included, has been sent: PHP-FPM reads the whole
body before running the script, so a slow upload does not count against it.

## Connections

Each `php_backend` has a pool of reusable connections per configuration
generation. A reload builds a new pool; the old one closes when its last request
finishes. An idle connection the backend closed is noticed and not reused.

## Managed processes (`command`)

```
php_backend "app" {
    command "C:/php/php-cgi.exe"
    processes 4
    max_requests 500
}
```

LOTUS starts `processes` copies of php-cgi and waits until each accepts
connections before it starts serving; if one can't start, the server doesn't
start and says why. php-cgi handles one request at a time, so `processes` is how
many PHP requests can run at once. A request waits for a free process for at most
`timeout`, then gets a 503.

- **Restarts.** A process that exits is started again. One that keeps crashing
  is restarted after a delay that doubles from 100 ms to 10 s, so a broken PHP
  install can't spin the CPU. A request that finds a process gone moments ago
  (nothing had been sent) simply goes to another one.
- **Recycling.** After `max_requests` requests a process is replaced while it is
  idle, which contains memory leaks. LOTUS counts requests itself and sets
  php-cgi's own `PHP_FCGI_MAX_REQUESTS` to 0: a process that exits by itself can
  do so just as a request reaches it, and that request would fail.
- **Reloads.** A reload that doesn't change the backend keeps the same
  processes. Changing `command`, `processes`, `max_requests`, `php_ini` or
  `timeout` starts a new pool, and the old one stops when its last request ends.
  If the new pool can't start, the reload is refused and the old one keeps running.
- **Stopping.** Processes stop with the server, after running requests finish.
  On Windows they are also placed in a job object, so they die with LOTUS even if
  it is killed from the Task Manager. On Unix there is no equivalent that is safe to use, so a server killed with
  `kill -9` leaves its processes running; stop it with `lotus stop` or SIGTERM.
- **Environment.** The processes get a short allowlist (`PATH`, `SystemRoot`,
  `TEMP`, `LANG`, `PHPRC`, ...), not the server's environment, which can hold
  secrets read with `env()`.
- **Output.** What php-cgi prints is logged as warnings, at most 20 lines a second.

### Who can reach the processes

On Unix each process listens on a Unix socket in a private directory (mode 0700).
On Windows php-cgi can only listen on TCP, so each one listens on
`127.0.0.1:<random port>`, **and any account on that machine can connect to it
and run PHP as the LOTUS user.** That is fine on a single-user machine or a
development box. On a shared Windows server, run LOTUS as a dedicated low-privilege
account and don't put sites of different owners on one pool; per-site accounts
come with the multi-tenant phase.
