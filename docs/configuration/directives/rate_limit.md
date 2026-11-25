# `rate_limit`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
rate_limit { ... }
```

| | |
|---|---|
| **Contexts** | `route`, `site` |
| **Block** | required, with the directives of the `rate_limit` context |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Limits how many requests a client may make. Requests over the limit get 429 with Retry-After. In a site it counts every request of the site; in a route it adds a limit for that route (both apply).

### Security

Counted per client address in memory, with a bounded table. Behind a proxy, list it in trusted_proxies or all clients share one allowance.
