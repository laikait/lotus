# `timeout`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

## In `health_check`

```text
timeout <duration>
```

| | |
|---|---|
| **Arguments** | `duration`: duration |
| **How often** | at most once |
| **Default** | 3s |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Time a probe may take.

### Security

None.

### Example

```text
timeout 2s
```

## In `php_backend`

```text
timeout <duration>
```

| | |
|---|---|
| **Arguments** | `duration`: duration |
| **How often** | at most once |
| **Default** | 60s |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Time to wait for the FastCGI server to respond. With command it is also how long a request waits for a free process.

### Security

Long timeouts let slow scripts tie up connections.

### Example

```text
fastcgi_pass "127.0.0.1:9000"
timeout 30s
```
