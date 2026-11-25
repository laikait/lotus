# `path`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

## In `health_check`

```text
path <path>
```

| | |
|---|---|
| **Arguments** | `path`: string |
| **How often** | at most once |
| **Default** | none (connect only) |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

URL path requested with GET. A status from 200 to 399 is healthy.

### Security

None.

### Example

```text
path "/healthz"
```

## In `metrics`

```text
path <path>
```

| | |
|---|---|
| **Arguments** | `path`: string |
| **How often** | at most once |
| **Default** | /metrics |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

URL path that serves the metrics. Any other path is 404.

### Security

None.

### Example

```text
address "127.0.0.1:9100"
path "/internal/metrics"
```
