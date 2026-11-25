# `lock_timeout`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
lock_timeout <duration>
```

| | |
|---|---|
| **Contexts** | `route_cache` |
| **Arguments** | `duration`: duration |
| **How often** | at most once |
| **Default** | 5s |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

When several requests miss on the same URL at once, one goes to the backend and the others wait up to this long for its answer. 0s lets every request go to the backend.

### Security

Without the wait, a popular page that expires can send a burst of identical requests to the backend.
