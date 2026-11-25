# `per`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
per <duration>
```

| | |
|---|---|
| **Contexts** | `rate_limit` |
| **Arguments** | `duration`: duration |
| **How often** | at most once |
| **Default** | 1s |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

The period requests counts over.

### Security

None.

### Example

```text
requests 100
per 1m
```
