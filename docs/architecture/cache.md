# Response cache

Status: built for routes that proxy (`proxy`) or run PHP (`php`). Not built: a disk
tier, background revalidation (`stale-while-revalidate`), revalidating a stale entry
with the backend (`If-None-Match`), purge by tag, a custom cache key, caching of
static files (the operating system already caches those), sharing a cache between
LOTUS processes.

A cache keeps a copy of a response and sends it again to the next visitor who asks
for the same URL, so the backend (an application server, or PHP) does the work once.
It is the difference between a WordPress site that falls over at a few dozen
visitors a second and one that serves thousands from memory.

## Configuration

A **zone** is a memory budget. It belongs to the server (only an administrator can
define one). A route uses a zone by naming it.

```
server {
    cache "pages" {
        max_size 256mib            # default 64mib
        max_entry_size 4mib        # default 1mib
    }

    site "example.com" {
        route "/blog/" {
            proxy "app"
            cache "pages" {
                ttl 5m                       # for responses that give no lifetime of their own
                negative_ttl 30s             # the same for 404 and 410 (default: not kept)
                stale_if_error 1h            # serve an old copy when the backend fails
                lock_timeout 5s              # one request fetches, the others wait
                bypass_cookies [ "wordpress_logged_in_*", "PHPSESSID" ]
            }
        }
    }
}
```

See `configs/examples/cache.conf` for a commented configuration. `cache` is an error in a route that has no `proxy` and no `php`: only what a backend
produced is kept.

| Directive | Default | Meaning |
|---|---|---|
| `max_size` | `64mib` | Memory for the zone's bodies, headers and bookkeeping, together with responses that are being copied. When it is full the least recently used entries go. |
| `max_entry_size` | `1mib` | Largest body that is kept. Bigger responses are passed on and not stored. Can't exceed `max_size`. |
| `ttl` | `0s` | Lifetime of a response that has none of its own. With `0s`, only responses that say how long they are good for (`max-age`, `s-maxage`, `Expires`) are kept. |
| `negative_ttl` | `0s` | The same for 404 and 410. |
| `stale_if_error` | `0s` | How long past its lifetime an entry may still be served when the backend fails. |
| `lock_timeout` | `5s` | How long a request waits for one that is fetching the same URL. `0s` turns waiting off. |
| `bypass_cookies` | `any` | `any`: a request with a cookie never uses the cache. `none`: cookies are ignored. A list: only the named cookies matter (a trailing `*` matches a prefix). |

## Which requests use it

A request uses the cache only when all of this holds:

- it is a `GET` or a `HEAD` (a `HEAD` is answered from a stored `GET`; on a miss it
  goes to the backend and stores nothing);
- it has no `Authorization` header and doesn't ask for an `Upgrade` (WebSocket);
- it has no body;
- `bypass_cookies` doesn't exclude it (by default any `Cookie` header does);
- for a miss to be **stored**, it is a whole-body request: a `Range` request on a miss
  goes to the backend and stores nothing. On a hit, `Range`, `If-None-Match` and
  `If-Modified-Since` are answered from the stored copy (206 and 304).

Other requests pass through: the response carries `X-Cache: BYPASS` when it was the
request that kept it out, and nothing is stored. The client's own `Cache-Control:
no-cache` or `max-age=0` is **ignored**, as it is by Nginx: a visitor must not be able
to make every request reach the backend.

**Writes invalidate.** A successful `POST`, `PUT`, `PATCH` or `DELETE` (status 200 to
399) to a URL removes everything stored for that path (every query string, scheme and
variant), as RFC 9111 section 4.4 asks. Pages that list or show the thing that
changed show it at once.

## What is stored

A response is stored only when it can be shared and has a lifetime:

| It is stored | It is never stored |
|---|---|
| status 200, 203, 204, 301, 308 | 206 and other statuses (302, 307, 4xx except below, 5xx) |
| 404 and 410, with `negative_ttl` or their own lifetime | `Cache-Control: private`, `no-store` or `no-cache` (even with field names) |
| lifetime from `s-maxage`, else `max-age`, else `Expires`, else `ttl` | a response with `Set-Cookie` (a session) |
| the entire body (a body cut short by the backend is dropped) | `Vary: *`, or a response that varies on more than six headers |
| a body up to `max_entry_size` | a `Trailer`, a `Content-Range`, `text/event-stream` |
| | a response the client didn't receive in full |

`max-age=0`, a past `Expires`, an invalid lifetime and an `Age` as big as the
lifetime all mean "not fresh", so nothing is stored. Where the backend's headers
disagree (two `max-age` values) the smaller one wins.

A response copied while it streams does not slow the client: the copy is made as the
bytes pass. If it grows past `max_entry_size`, or too many responses are being copied
at once (together they may use at most `max_size`), the copy is dropped and the
response goes on to the client as it would have without a cache.

## The key

Two requests are the same when they have the same **scheme**, **host name** (the
site's, lower case, without a port), **path** and **query string** as the client sent
them, and the same values in the request headers the response names in `Vary`. The
scheme is part of the key because a backend may answer http and https differently (a
redirect to https).

- The key comes from the request as the client sent it, not from what routing made of
  it, so a front controller (`/pretty/url` handled by `index.php`) caches each pretty
  URL separately.
- `Vary: Accept-Encoding` is compared as the set of codings the client accepts, so
  `gzip, br`, `br,gzip` and `br;q=1, gzip;q=0.5` share a copy.
- A URL keeps at most eight variants; the oldest goes when a ninth arrives, so a
  header with endless values can't fill the zone.
- Every site has its own entries even in a shared zone; a page of one site is never
  served for another.

## Freshness and old copies

A fresh entry is served without asking the backend (`X-Cache: HIT`). When it is past
its lifetime the next request goes to the backend (`MISS`) and replaces it.

With `stale_if_error`, an entry stays for that long after its lifetime, and covers for
a backend that fails: a `500`, `502`, `503` or `504` answer, or no answer at all
(connection refused, timeout) on a proxy route. The client gets the old copy with
`X-Cache: STALE` and the log says so. Other statuses (400, 404, 501...) are the
backend's real answer and are passed on. A response with `must-revalidate` is never
served stale. A failure inside PHP's own gateway (FastCGI unreachable) is not covered;
a script that returns a 5xx is.

## One fetch for many requests

When a popular page expires, hundreds of requests may ask for it in the same
moment. With `lock_timeout` the first goes to the backend and the others wait for its
answer, so the backend sees one request. When the answer is stored they are served
from it (`HIT`). When it can't be stored (private, an error, too big) they are
released as soon as the status is known and each fetches for itself, so one slow
uncacheable page does not queue its visitors. A waiter that has waited
`lock_timeout` goes to the backend without storing. When the backend failed and an
old copy exists, the waiters get that copy.

## Compression

Compression (`compression.md`) sits outside the cache: a stored page is kept as the backend
sent it, and each client gets it compressed as it asks. A `Range` request on a hit is
answered from the stored bytes and is not compressed.

## Headers

- `X-Cache: HIT | MISS | STALE | BYPASS`. A backend's own `X-Cache` is replaced.
- `Age` on a stored response: seconds since it was made (the backend's own `Age`
  counts).
- Headers the server sets itself (`Server`, the site's `security_headers`) are never
  replaced by stored ones, the same rule as for any proxied response.
- `Date`, `Connection`, `Content-Length` and other hop-by-hop headers are not stored;
  they are made for each response.

## Seeing and emptying it

```
$ lotus cache status
ZONE   ENTRIES  SIZE      LIMIT      HIT RATE  HITS   MISSES  STALE  BYPASS  EVICTED
pages  1204     38.2 MiB  256.0 MiB  93.1%     93110  6890    2      4021    7

$ lotus cache purge --path /blog/hello          # one page, every query string
$ lotus cache purge --prefix /blog/ --host example.com
$ lotus cache purge --all
purged 12 entries
```

`--zone` names the zone and can be left out when there is only one. `--all` must be
written; a purge without `--path`, `--prefix` or `--all` is refused. A path is the
URL path as a browser sends it (percent-encoded). The hit rate counts hits against
misses; requests that bypassed the cache are not in it. `lotus.cache.status` and
`lotus.cache.purge` are the control methods (see `control-protocol.md`); a purge is
audited in the log. `lotus config explain` says whether a route's cache applies to a
request.

Metrics, by zone (labels are zone names from the configuration):

| Series | Type | Meaning |
|---|---|---|
| `lotus_cache_requests_total{zone,result}` | counter | `hit`, `miss`, `stale`, `bypass` |
| `lotus_cache_entries{zone}` | gauge | responses stored |
| `lotus_cache_size_bytes{zone}`, `lotus_cache_max_size_bytes{zone}` | gauge | memory used and the limit |
| `lotus_cache_stored_total{zone}` | counter | responses added |
| `lotus_cache_evictions_total{zone}` | counter | dropped to make room; a steady rate means the zone is too small |
| `lotus_cache_purged_total{zone}` | counter | removed by a purge or a write |

## Reloads

A zone belongs to the server, not to one configuration: a reload keeps what is stored.
A zone that is resized gets its new limit at once (a smaller one evicts at once), and a
zone that is gone from the configuration is dropped with its entries. A route that
changes zone starts from that zone's contents.

## Security

A cache sends one visitor's response to another, so every rule above errs on the side
of not storing:

- A request with credentials never reaches the cache, and a response that sets a
  cookie or is marked private is never kept. `bypass_cookies` defaults to `any`:
  switch it to `none` or to a list only for routes where cookies (analytics, a theme)
  don't change the page, or name the session cookie.
- The key has only what the client sent in the URL, the host and the headers the
  response varies on. **A backend that changes its answer by a header it doesn't list
  in `Vary`** (the user agent, a custom header) will have one variant served to
  everyone. LOTUS sets `X-Forwarded-*` itself, so those can't be forged to poison an
  entry.
- Static files are never cached here, so a file's access rules (`.htaccess`, dotfile
  and symlink policy) can't be bypassed by an old copy.
- Memory is bounded: by `max_size` for stored entries, and by the same limit for
  responses being copied. Labels in metrics are zone names, never client input.
- Tenants (included files) can neither define a zone nor turn the cache on.

## Limits

- Memory only. A restart empties the cache (a reload does not).
- Entries expire lazily: an expired entry that nobody asks for stays until the space
  is needed.
- A stale entry is replaced after a miss, not refreshed in the background, and not
  revalidated with `If-None-Match`; the backend sends the full response again.
- `lotus cache purge` walks the whole zone, so a purge in a zone of hundreds of
  thousands of entries takes a few milliseconds while the zone is locked. The
  invalidation by a write (a `POST` to the URL) is a direct lookup and costs nothing
  like that.
- One lock guards a zone. Hits take it for a map lookup and a list update; zones for
  unrelated sites can be separate to share nothing.
