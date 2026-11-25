# `fail_timeout`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
fail_timeout <duration>
```

| | |
|---|---|
| **Contexts** | `upstream` |
| **Arguments** | `duration`: duration |
| **How often** | at most once |
| **Default** | 10s |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

The window in which max_fails failures are counted, and how long a backend stays out afterwards.

### Security

None.
