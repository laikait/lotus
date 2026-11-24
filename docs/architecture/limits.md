# Rate limits and connection limits

Three protections against clients that ask for too much, all in memory, all
bounded, all keyed by the **resolved client address**: the connection's peer,
or, when that peer is in `trusted_proxies`, the address it reports. IPv6 clients
are counted per **/64**, because a single customer usually has a whole /64 and
could otherwise use a fresh key for every request.

## Connections per address

```
server {
    limits {
        max_connections 10000       # all clients together
        max_connections_per_ip 50   # one address
    }
}
```

A connection over the limit is closed at once, before any bytes are read. The
count is of *open connections*, not of requests. Server-wide only.

Connections from `trusted_proxies` are **not counted**: every client of such a
proxy arrives from the proxy's address, so counting it would limit all of them
together. Limit what those clients do with `rate_limit` instead.

## Request rate limits

```
site "example.com" {
    rate_limit {              # every request of the site
        requests 300
        per 1m
        burst 60
    }
    route "/login" {
        rate_limit {          # and, for this route, also this
            requests 5
            per 1m
            key ip
        }
    }
}
```

- `requests` per `per` is the long-run rate. `burst` (default `requests`) is how
  many may arrive at once after a quiet time; the allowance then refills
  continuously (a token bucket).
- `key ip` (default) gives each client address its own allowance; `key global`
  shares one among everybody (a cap on the route's total load).
- A request is checked against the site's limit and the route's, if it has one.
  Both must allow it. A route's limit adds to the site's; it never replaces it.
- A refused request gets **429** with `Retry-After` (seconds, at least 1) and
  goes no further: no file is read, no script runs, no backend is asked, no
  password is checked. It is logged in the access log with status 429.
- The check comes before `.htaccess`, so a client over its limit can't use
  password checks to burn CPU.
- A request that is routed again inside LOTUS (a rewrite, `try_files`) is counted
  once per limit, not once per step. The page of an error being served is not
  counted.
- The 429 page is plain text; `error_page 429` isn't applied (the limiter answers
  before error pages are set up).
- Only administrators can set `rate_limit`: a tenant's included file can't, so it
  can't loosen what the administrator set.

## Wrong passwords (HTTP Basic)

```
site "example.com" {
    htaccess true
    htaccess_auth_failures 10   # wrong attempts ...
    htaccess_auth_lockout 5m    # ... within this time lock the address out for this long
}
```

Defaults: 10 and 5 minutes. After 10 wrong attempts from one address within 5
minutes, requests from that address to password-protected places get **429**
with `Retry-After` until the lock-out ends, and their passwords aren't checked
(even a right one). A right password resets the count. A request without
credentials is only asked for them; that isn't a failure. `htaccess_auth_failures
0` turns it off.

The count is of the client address, not the user name, so one attacker can't lock
a user out for everyone else, but several users behind one NAT address share the
count.

## Memory

Every table is bounded: 65 536 keys per limiter (per site, per route, per lock-out),
spread over 64 shards. Idle entries are dropped as they are found; if a table is still
full when a new key arrives, an old one is dropped (chosen from a small random
sample, so a flood of new keys can't make each insert expensive). An attacker who
fills a table can therefore reset other clients' counters, but only by sending
that many requests from that many /64s.

## Not built yet

Per-site connection limits, limits by header or cookie, a limit on bytes per
second, a shared state between several LOTUS processes, rate-limit metrics
(rejections are in the access log), and `X-RateLimit-*` headers.
