# `read_timeout`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
read_timeout <duration>
```

| | |
|---|---|
| **Contexts** | `limits` |
| **Arguments** | `duration`: duration |
| **How often** | at most once |
| **Default** | 60s |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Time allowed to receive the whole request.

### Security

Long values make slow-upload attacks easier.
