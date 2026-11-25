# `idle_timeout`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

## In `limits`

```text
idle_timeout <duration>
```

| | |
|---|---|
| **Arguments** | `duration`: duration |
| **How often** | at most once |
| **Default** | 120s |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Time an idle keep-alive connection is kept open.

### Security

Long values let idle clients hold connections.

### Example

```text
idle_timeout 1m
```

## In `upstream`

```text
idle_timeout <duration>
```

| | |
|---|---|
| **Arguments** | `duration`: duration |
| **How often** | at most once |
| **Default** | 90s |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

How long an unused connection to a backend is kept for reuse.

### Security

None.

### Example

```text
idle_timeout 30s
```
