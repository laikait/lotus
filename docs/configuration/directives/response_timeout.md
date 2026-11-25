# `response_timeout`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
response_timeout <duration>
```

| | |
|---|---|
| **Contexts** | `upstream` |
| **Arguments** | `duration`: duration |
| **How often** | at most once |
| **Default** | 60s |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Time to wait for a backend's response headers after the request was sent. A request that times out is not repeated.

### Security

Long timeouts let slow backends tie up connections.

### Example

```text
response_timeout 30s
```
