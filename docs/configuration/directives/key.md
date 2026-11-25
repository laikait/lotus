# `key`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
key <key>
```

| | |
|---|---|
| **Contexts** | `rate_limit` |
| **Arguments** | `key`: one of ip|global |
| **How often** | at most once |
| **Default** | ip |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

What is counted separately: ip gives each client address its own allowance (IPv6 per /64, after trusted_proxies); global shares one allowance among all clients.

### Security

With ip behind a proxy that is not in trusted_proxies, every client counts as the proxy and shares one allowance.

### Example

```text
requests 1000
per 1s
key global
```
