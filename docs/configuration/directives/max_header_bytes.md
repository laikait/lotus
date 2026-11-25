# `max_header_bytes`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
max_header_bytes <size>
```

| | |
|---|---|
| **Contexts** | `limits` |
| **Arguments** | `size`: size |
| **How often** | at most once |
| **Default** | 64kib |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Maximum total size of the request line and headers. A request over the limit gets 431. Go's HTTP server reads a little more before it stops (about 4 KiB), so the effective limit is slightly above this value.

### Security

Large values make header-based memory exhaustion easier.

### Example

```text
max_header_bytes 32kib
```
