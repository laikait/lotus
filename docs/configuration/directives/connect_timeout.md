# `connect_timeout`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
connect_timeout <duration>
```

| | |
|---|---|
| **Contexts** | `upstream` |
| **Arguments** | `duration`: duration |
| **How often** | at most once |
| **Default** | 5s |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Time allowed to connect to a backend (and finish its TLS handshake).

### Security

A long timeout keeps requests waiting on a backend that is down.
