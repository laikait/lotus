# `negative_ttl`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
negative_ttl <duration>
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

How long a 404 or 410 response is kept when the backend gives no lifetime. With 0s they are not kept.

### Security

A page that appears after a miss stays invisible for this long.
