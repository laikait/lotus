# `header_timeout`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
header_timeout <duration>
```

| | |
|---|---|
| **Contexts** | `limits` |
| **Arguments** | `duration`: duration |
| **How often** | at most once |
| **Default** | 15s |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Time allowed to receive the request headers after a connection is established.

### Security

Long values make slowloris attacks easier.

### Example

```text
header_timeout 10s
```
