# `write_timeout`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
write_timeout <duration>
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

Time allowed to send a response.

### Security

Long values let slow readers hold connections open.

### Example

```text
write_timeout 30s
```
