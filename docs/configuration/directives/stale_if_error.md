# `stale_if_error`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
stale_if_error <duration>
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

How long past its lifetime an entry may still be served when the backend fails (500, 502, 503, 504 or no answer). With 0s a failing backend's errors are passed on. Responses with must-revalidate are never served stale.

### Security

Visitors may see old content while the backend is down. That is usually better than an error page.
