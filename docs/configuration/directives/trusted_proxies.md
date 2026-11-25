# `trusted_proxies`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
trusted_proxies <networks>
```

| | |
|---|---|
| **Contexts** | `server` |
| **Arguments** | `networks`: list of IP address or CIDR |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Networks whose X-Forwarded-For / Forwarded headers and PROXY protocol headers are believed when working out the client IP.

### Security

Listing a network that clients can reach lets them fake their IP address and bypass IP allow-lists and rate limits. List only your own load balancers.

### Example

```text
trusted_proxies [ "10.0.0.0/8", "192.168.0.0/16" ]
```
