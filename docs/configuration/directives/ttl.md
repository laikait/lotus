# `ttl`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
ttl <duration>
```

| | |
|---|---|
| **Contexts** | `route_cache` |
| **Arguments** | `duration`: duration |
| **How often** | at most once |
| **Default** | 0s |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

How long a response stays fresh when the backend gives no lifetime of its own (no max-age, s-maxage or Expires). With 0s only responses that carry a lifetime are kept.

### Security

A response is served unchanged for this long. Content that changes sooner shows stale data.
