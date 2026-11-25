# Configuration reference

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

Every directive LOTUS understands, generated from the same registry the validator uses. See [the grammar](../grammar.md) for the syntax, and `lotus config validate` to check a file.

## Where directives go

```text
server
    cache
    compression
    control
    limits
    listen
    metrics
    php_backend
    runtime
    site
        compression
        limits
        logs
        rate_limit
        route
            cache
            match
            rate_limit
            route
        security_headers
    tls
        acme
            dns
    upstream
        backend
        health_check
```

A directive that appears in several places (for example `compression`, `cache` or `max_request_body`) has one page that lists them.

## All directives

| Directive | What it does | Where |
|---|---|---|
| [`access`](access.md) | File that receives the access log. | `logs` |
| [`acme`](acme.md) | Automatic certificate issuance and renewal through ACME (for example Let's Encrypt). | `tls` |
| [`address`](address.md) | Address to listen on, as host:port, [ipv6]:port or :port. | `listen`, `metrics` |
| [`algorithms`](algorithms.md) | The codings LOTUS may use, most preferred first. | `compression` |
| [`aliases`](aliases.md) | Additional hostnames served by this site. | `site` |
| [`allow`](allow.md) | If set, only clients in these networks may read the metrics; others get 403. | `metrics` |
| [`api_token`](api_token.md) | Cloudflare API token with Zone.DNS:Edit (and Zone:Read unless limited to one zone). | `dns` |
| [`backend`](backend.md) | A server that requests are sent to: http://host:port, https://host:port or unix:/path. | `upstream` |
| [`balance`](balance.md) | How requests are spread over the backends. | `upstream` |
| [`bearer_token`](bearer_token.md) | If set, a request must carry `Authorization: Bearer TOKEN`. | `metrics` |
| [`burst`](burst.md) | How many requests may arrive at once after a quiet time. | `rate_limit` |
| [`bypass_cookies`](bypass_cookies.md) | Which request cookies keep a request out of the cache: any (a request with a cookie never uses it), none (cookies are ignored), or a list of cookie names, where a trailing * matches a prefix, such as [ "wordpress_logged_in_*", "PHPSESSID" ]. | `route_cache` |
| [`ca`](ca.md) | ACME directory URL of the certificate authority. | `acme` |
| [`ca_root`](ca_root.md) | PEM file with the root certificate of a private or test CA (such as Pebble), trusted only for talking to the ACME server. | `acme` |
| [`cache`](cache.md) | Keeps the responses of this route's proxy or PHP in the named cache zone and answers repeated GET requests from there. | `route`, `server` |
| [`certificate`](certificate.md) | PEM certificate chain file for tls manual. | `site` |
| [`certificate_key`](certificate_key.md) | PEM private key file that belongs to certificate. | `site` |
| [`challenges`](challenges.md) | Which validations LOTUS offers the CA. | `acme` |
| [`client_ip`](client_ip.md) | True if the client IP is in one of the networks (with !~: if it is in none of them). | `match` |
| [`command`](command.md) | Program for the exec provider. | `dns`, `php_backend` |
| [`compression`](compression.md) | Compresses responses (Brotli, Zstandard, gzip) for clients that accept it. | `server`, `site` |
| [`connect_timeout`](connect_timeout.md) | Time allowed to connect to a backend (and finish its TLS handshake). | `upstream` |
| [`control`](control.md) | Settings for the local control channel used by the lotus command. | `server` |
| [`cookie`](cookie.md) | Tests a cookie. | `match` |
| [`dns`](dns.md) | How LOTUS publishes the DNS record for a dns-01 validation. | `acme` |
| [`dotfiles`](dotfiles.md) | Whether files and directories whose names start with a dot (such as .git or .env) can be served. | `site` |
| [`eab_hmac_key`](eab_hmac_key.md) | External account binding HMAC key, for CAs that require it. | `acme` |
| [`eab_key_id`](eab_key_id.md) | External account binding key ID, for CAs that require it. | `acme` |
| [`email`](email.md) | Contact email registered with the certificate authority. | `acme` |
| [`enabled`](enabled.md) | With false, compression is off (in a site: for this site even if the server's block turns it on). | `compression` |
| [`error`](error.md) | File that receives this site's error log. | `logs` |
| [`error_page`](error_page.md) | Shows another page for responses with this status that LOTUS produces itself (a missing file, return or try_files =404). | `route`, `site` |
| [`fail_timeout`](fail_timeout.md) | The window in which max_fails failures are counted, and how long a backend stays out afterwards. | `upstream` |
| [`fastcgi_pass`](fastcgi_pass.md) | FastCGI server address: unix:/path/to/socket or host:port. | `php_backend` |
| [`header`](header.md) | Tests a request header. | `match` |
| [`header_timeout`](header_timeout.md) | Time allowed to receive the request headers after a connection is established. | `limits` |
| [`health_check`](health_check.md) | Probes every backend regularly and takes failing ones out of rotation until they recover. | `upstream` |
| [`healthy_threshold`](healthy_threshold.md) | Successful probes in a row that bring a backend back. | `health_check` |
| [`host`](host.md) | True if the Host header is one of these. | `match` |
| [`htaccess`](htaccess.md) | Reads .htaccess files in the document root and the directories below it, for the directives in htaccess_allow. | `site` |
| [`htaccess_allow`](htaccess_allow.md) | Which kinds of directives .htaccess files may use: rewrite, redirect, error_document, directory_index, options, access, headers, php. | `site` |
| [`htaccess_auth_failures`](htaccess_auth_failures.md) | After this many wrong Basic authentication attempts from one client address within htaccess_auth_lockout, further attempts get 429 until the lock-out ends, without checking passwords. | `site` |
| [`htaccess_auth_lockout`](htaccess_auth_lockout.md) | The window in which wrong attempts are counted, and how long a client is locked out afterwards. | `site` |
| [`htaccess_recheck`](htaccess_recheck.md) | How long a .htaccess file (or its absence) is remembered before the file system is looked at again. | `site` |
| [`htaccess_unsupported`](htaccess_unsupported.md) | What happens when a .htaccess file holds a directive LOTUS doesn't support or the site doesn't allow: error answers 500 and logs the directive; ignore skips it with a warning. | `site` |
| [`http3`](http3.md) | Also serve HTTP/3 over UDP on the same port number and advertise it with Alt-Svc. | `listen` |
| [`http_port`](http_port.md) | Port the CA connects to for HTTP-01 validation, if it is not 80 (for example behind a port forward). | `acme` |
| [`idle_timeout`](idle_timeout.md) | Time an idle keep-alive connection is kept open. | `limits`, `upstream` |
| [`index`](index.md) | Files to try, in order, when a request names a directory. | `route`, `site` |
| [`interval`](interval.md) | Time between probes of a backend. | `health_check` |
| [`key`](key.md) | What is counted separately: ip gives each client address its own allowance (IPv6 per /64, after trusted_proxies); global shares one allowance among all clients. | `rate_limit` |
| [`level`](level.md) | default balances size and CPU for responses made on the fly; fast uses the least CPU; best the most. | `compression` |
| [`limits`](limits.md) | Connection, size and timeout limits. | `server`, `site` |
| [`listen`](listen.md) | A network listener. | `server` |
| [`listeners`](listeners.md) | Names of the listeners this site answers on. | `site` |
| [`lock_timeout`](lock_timeout.md) | When several requests miss on the same URL at once, one goes to the backend and the others wait up to this long for its answer. | `route_cache` |
| [`logs`](logs.md) | Per-site log files. | `site` |
| [`match`](match.md) | Applies different actions when extra conditions hold. | `route` |
| [`max_connections`](max_connections.md) | Maximum number of simultaneous connections. | `limits` |
| [`max_connections_per_ip`](max_connections_per_ip.md) | Maximum simultaneous connections from one client address (IPv6 clients are counted per /64). | `limits` |
| [`max_conns`](max_conns.md) | Most requests in progress on this backend. | `backend`, `upstream` |
| [`max_entry_size`](max_entry_size.md) | Largest response body that is stored. | `cache` |
| [`max_fails`](max_fails.md) | After this many failures within fail_timeout a backend is taken out of rotation for fail_timeout. | `upstream` |
| [`max_header_bytes`](max_header_bytes.md) | Maximum total size of the request line and headers. | `limits` |
| [`max_procs`](max_procs.md) | Maximum number of CPUs Go uses (GOMAXPROCS). | `runtime` |
| [`max_request_body`](max_request_body.md) | Largest request body accepted. | `limits`, `route` |
| [`max_requests`](max_requests.md) | A php-cgi process exits and is replaced after this many requests, which contains memory leaks. | `php_backend` |
| [`max_size`](max_size.md) | Most memory the zone's stored responses (bodies, headers and bookkeeping) may use. | `cache` |
| [`memory_limit`](memory_limit.md) | Soft memory limit for the Go runtime (GOMEMLIMIT). | `runtime` |
| [`method`](method.md) | True if the request method is one of these. | `match` |
| [`metrics`](metrics.md) | A separate listener that serves metrics in the Prometheus text format (requests, latency, connections, backends, certificates). | `server` |
| [`min_size`](min_size.md) | Smallest body that is compressed. | `compression` |
| [`min_version`](min_version.md) | Lowest TLS version accepted. | `tls` |
| [`negative_ttl`](negative_ttl.md) | How long a 404 or 410 response is kept when the backend gives no lifetime. | `route_cache` |
| [`path`](path.md) | URL path requested with GET. | `health_check`, `metrics` |
| [`per`](per.md) | The period requests counts over. | `rate_limit` |
| [`php`](php.md) | Runs matching requests through the named php_backend over FastCGI. | `match`, `route` |
| [`php_backend`](php_backend.md) | A FastCGI backend (for example a PHP-FPM pool). | `server` |
| [`php_deny`](php_deny.md) | When true, PHP files under this route are never executed, even if a PHP backend is configured. | `match`, `route` |
| [`php_ini`](php_ini.md) | php.ini file for the processes started with command (php-cgi -c). | `php_backend` |
| [`precompressed`](precompressed.md) | For files served from disk, send file.css.br, file.css.zst or file.css.gz (made beforehand, one per algorithm) in place of file.css when the client accepts that coding, with no CPU spent per request. | `compression` |
| [`processes`](processes.md) | How many php-cgi processes to run with command. | `php_backend` |
| [`propagation_check`](propagation_check.md) | Check, before asking the CA to validate, that the record can be seen in DNS. | `dns` |
| [`propagation_delay`](propagation_delay.md) | Time to wait after the record is published before checking that it is visible. | `dns` |
| [`propagation_timeout`](propagation_timeout.md) | How long to wait for the record to become visible before the CA is asked to check. | `dns` |
| [`protocol`](protocol.md) | http serves plain HTTP; https serves TLS (HTTP/1.1 and HTTP/2). | `listen` |
| [`provider`](provider.md) | cloudflare sets the record through the Cloudflare API. | `dns` |
| [`proxy`](proxy.md) | Sends matching requests to the named upstream (a reverse proxy). | `match`, `route` |
| [`proxy_host`](proxy_host.md) | Which Host header the upstream gets: client keeps the one the client sent (and the application sees its own public name); upstream uses the backend's own address. | `route` |
| [`proxy_set_header`](proxy_set_header.md) | Sets a header on the request sent to the upstream. | `route` |
| [`query`](query.md) | Tests a query-string parameter. | `match` |
| [`rate_limit`](rate_limit.md) | Limits how many requests a client may make. | `route`, `site` |
| [`read_timeout`](read_timeout.md) | Time allowed to receive the whole request. | `limits` |
| [`requests`](requests.md) | How many requests are allowed per period (see per). | `rate_limit` |
| [`resolvers`](resolvers.md) | DNS servers (host:port) asked whether the record is visible and which zone a name belongs to. | `dns` |
| [`response_timeout`](response_timeout.md) | Time to wait for a backend's response headers after the request was sent. | `upstream` |
| [`retries`](retries.md) | How many other backends a request may be tried on after a failure. | `upstream` |
| [`return`](return.md) | Answer immediately with a status code. | `match`, `route` |
| [`rewrite`](rewrite.md) | If the request path matches the pattern, replaces the request: with last (the default) routing starts again with the new URI, redirect answers 302 and permanent answers 301. | `route` |
| [`root`](root.md) | Document root: the directory that URLs are mapped into. | `route`, `site` |
| [`route`](route.md) | Handles requests whose path matches. | `route`, `site` |
| [`runtime`](runtime.md) | Go runtime settings for the LOTUS process. | `server` |
| [`scheme`](scheme.md) | True if the request used this scheme. | `match` |
| [`security_headers`](security_headers.md) | Response headers added to every response, written as "Name" = "value" entries. | `site` |
| [`server`](server.md) | Top-level container for listeners, TLS settings, PHP backends and sites. | `root` |
| [`site`](site.md) | A virtual host. | `server` |
| [`socket`](socket.md) | Path of the control socket (named pipe name on Windows). | `control` |
| [`stale_if_error`](stale_if_error.md) | How long past its lifetime an entry may still be served when the backend fails (500, 502, 503, 504 or no answer). | `route_cache` |
| [`storage`](storage.md) | Directory for the ACME account key, certificates and their private keys. | `acme` |
| [`symlinks`](symlinks.md) | How symbolic links below the document root are treated: deny refuses them, owner_match follows a link only if the link and its target have the same owner, follow allows them. | `site` |
| [`timeout`](timeout.md) | Time a probe may take. | `health_check`, `php_backend` |
| [`tls`](tls.md) | Server-wide TLS settings. | `server`, `site` |
| [`tls_alpn_port`](tls_alpn_port.md) | Port the CA connects to for TLS-ALPN-01 validation, if it is not 443. | `acme` |
| [`tls_ca`](tls_ca.md) | PEM file of the authority that signed the backends' certificates. | `upstream` |
| [`tls_client_cert`](tls_client_cert.md) | Certificate that LOTUS presents to https backends that require client certificates (with tls_client_key). | `upstream` |
| [`tls_client_key`](tls_client_key.md) | Private key for tls_client_cert. | `upstream` |
| [`tls_server_name`](tls_server_name.md) | Name to expect in the backend's certificate and to send as SNI. | `upstream` |
| [`tls_verify`](tls_verify.md) | Check the certificate of https backends against the trusted authorities and the backend's name. | `upstream` |
| [`trusted_proxies`](trusted_proxies.md) | Networks whose X-Forwarded-For / Forwarded headers and PROXY protocol headers are believed when working out the client IP. | `server` |
| [`try_files`](try_files.md) | Tries each candidate as a file or directory in the site root. | `match`, `route` |
| [`ttl`](ttl.md) | How long a response stays fresh when the backend gives no lifetime of its own (no max-age, s-maxage or Expires). | `route_cache` |
| [`types`](types.md) | The media types that are compressed: exact ("application/json") or a whole family ("text/*"). | `compression` |
| [`unhealthy_threshold`](unhealthy_threshold.md) | Failed probes in a row that take a backend out. | `health_check` |
| [`upstream`](upstream.md) | A group of backend servers that routes send requests to with the proxy directive, with load balancing and health checks. | `server` |
| [`viewer_group`](viewer_group.md) | OS group whose members get read-only access to the control channel. | `control` |
| [`weight`](weight.md) | Share of requests relative to the other backends (round_robin, ip_hash, uri_hash, random and least_conn). | `backend` |
| [`write_timeout`](write_timeout.md) | Time allowed to send a response. | `limits` |
