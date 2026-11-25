# `bypass_cookies`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
bypass_cookies <cookies>
```

| | |
|---|---|
| **Contexts** | `route_cache` |
| **Arguments** | `cookies`: one of any|none or list of string |
| **How often** | at most once |
| **Default** | any |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Which request cookies keep a request out of the cache: any (a request with a cookie never uses it), none (cookies are ignored), or a list of cookie names, where a trailing * matches a prefix, such as [ "wordpress_logged_in_*", "PHPSESSID" ].

### Security

A request with credentials in a cookie must never be answered from, or stored in, a cache that other visitors use. Requests with an Authorization header are never cached whatever this says.
