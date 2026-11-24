# Reverse proxy

Status: the core of Phase 4. Responses can be cached (`cache`, see `cache.md`).
Not built yet: buffering of request bodies to disk, `Forwarded` generation, PROXY protocol to backends,
`proxy_pass` translation from Nginx, sticky sessions by cookie, HTTP/3 to
backends, per-upstream metrics.

## Configuration

An `upstream` is a named group of backends, defined in `server` (only an
administrator can: a tenant's included file may not define or choose one). A
route sends requests to it with `proxy`.

```
server {
    upstream "app" {
        backend "http://10.0.0.5:8080" { weight 3 }
        backend "http://10.0.0.6:8080"
        backend "unix:/run/app.sock"

        balance least_conn          # round_robin (default), least_conn, random, ip_hash, uri_hash
        connect_timeout 5s
        response_timeout 60s
        retries 1
        max_fails 3
        fail_timeout 10s
        health_check { path "/healthz"
                       interval 10s }
    }

    site "example.com" {
        route "/api/" {
            proxy "app"
            proxy_set_header "X-Env" "production"
        }
    }
}
```

See `configs/examples/proxy.lotus`, and `lotus config docs` for every directive.

A route either proxies or serves files: `proxy` with `try_files` or `php` in the
same route is an error. `rewrite` rules of the route still run first, and the
rewritten path is what the backend receives. A `match` block may switch a route to
`proxy`.

## What the backend receives

- The method, the path (as rewritten) with its original query, the body, and the
  client's headers, except hop-by-hop headers (`Connection` and what it names,
  `Keep-Alive`, `TE` other than `trailers`, `Upgrade` unless this is a WebSocket
  request, ...).
- **`Host`** is the client's by default, so the application sees its public name.
  `proxy_host upstream` sends the backend's own address instead.
- **`X-Forwarded-For`, `X-Forwarded-Host`, `X-Forwarded-Proto`** are LOTUS's own
  and replace whatever the client sent. `X-Forwarded-For` holds one address: the
  client address LOTUS resolved (after `trusted_proxies`). `Forwarded`,
  `X-Real-IP` and `X-Forwarded-Port` from the client are removed. A request that
  came through another proxy therefore loses the intermediate hops; the backend
  trusts LOTUS and nothing else.
- `X-Request-Id`, the id in the access log.
- Headers from `proxy_set_header` (an empty value removes the header). The
  forwarding headers and framing headers can't be set that way.
- Response compression is not negotiated by LOTUS: `Accept-Encoding` goes through
  and the backend's answer comes back as it is.

## What the client receives

The backend's status, headers and body, streamed as they arrive (an event stream
or a slow download is not held back). Except:

- The backend's `Server` header is removed; LOTUS sends its own.
- The site's `security_headers` can't be overridden by the backend.
- `error_page` doesn't replace a response the backend produced (a backend 404 is
  the backend's page), as with PHP. Errors that LOTUS produces itself do use the
  site's error pages. **An error page must be served by a route that doesn't
  proxy**, or a failing backend would fail to produce its own error page too.

| Situation | Answer |
|---|---|
| Connection refused, reset or other transport error | 502 |
| Response headers took longer than `response_timeout` | 504 |
| Every backend down or full, or `max_conns` reached | 503 with `Retry-After: 1` |
| Request body over the limit | 413 |
| Client went away | 499 in the log |

A body that fails halfway after the status was sent cuts the connection, so the
client sees an incomplete response rather than a truncated "success".

## WebSocket and upgrades

A request with `Upgrade: websocket` (or any `Upgrade`) is forwarded as an upgrade;
after the backend answers `101` the two connections are joined and bytes flow both
ways until either side closes. Timeouts of the HTTP server for the request don't
cut an established tunnel.

## Choosing a backend

- `round_robin`: smooth weighted round robin. Weights 5:1:1 give a, a, b, a, c, a,
  a, not five a in a row.
- `least_conn`: fewest requests in progress per unit of weight.
- `random`: two random backends, the less loaded one ("power of two choices").
- `ip_hash`, `uri_hash`: the same client address or path goes to the same
  backend, by weighted rendezvous hashing. When a backend leaves, only the keys
  that were on it move.

Backends that are out of rotation or at their `max_conns` are never chosen.

## Failures, retries and health

**Passive.** A transport failure counts against the backend. After `max_fails`
failures within `fail_timeout` it is out for `fail_timeout`, then it gets
requests again. `max_fails 0` turns this off. A client that gave up is not a
failure. HTTP error statuses from a backend (500, 503, ...) are not counted:
those are the application's answers.

**Active.** With `health_check` every backend is probed each `interval`: a GET to
`path` (200 to 399 is healthy) or, without a path, a TCP connect. A backend goes
out after `unhealthy_threshold` failures in a row and comes back after
`healthy_threshold` successes. Backends start healthy. A backend that active
checks have taken out stays out until they pass again.

**Retries.** A failed request goes to another backend, up to `retries` more times,
only when repeating it is safe:

- the connection could not be made (the request was never sent), or
- the method is GET, HEAD, OPTIONS, PUT or DELETE, has no body, and failed before a
  response.

A POST whose body was read by a backend is never sent again. A request that timed
out is not repeated either (it might still be running).

## TLS to backends

`https://` backends are verified by default against the system certificates and
the backend's host name. `tls_ca` names the authority of a private network,
`tls_server_name` the name to check when the address is an IP, `tls_client_cert`
and `tls_client_key` present a client certificate. `tls_verify false` turns
verification off (logged at start); it makes the connection readable and
changeable by anyone on the path. HTTP/2 is used to `https` backends when they
offer it. Minimum TLS version is 1.2.

## Limits

- State (health, connection counts) belongs to a configuration generation: a
  reload starts fresh upstreams, and the old ones finish their requests.
- `max_conns` on an upstream or backend counts requests in progress; it doesn't
  queue. Beyond it requests get 503 at once.
- No request buffering: a request body is streamed to the backend, so a request
  that failed after the body started can't be repeated.
- Backend addresses are fixed at configuration time; DNS names are resolved when
  a connection is made (each new connection), not tracked.
- `.htaccess` files and the site's file system rules don't apply to a proxied
  route.
