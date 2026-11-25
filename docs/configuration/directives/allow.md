# `allow`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
allow <networks>
```

| | |
|---|---|
| **Contexts** | `metrics` |
| **Arguments** | `networks`: list of IP address or CIDR |
| **How often** | at most once |
| **Default** | any |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

If set, only clients in these networks may read the metrics; others get 403. Checked against the connection's address (not X-Forwarded-For).

### Security

Without bearer_token or allow, anyone who can reach the address can read the metrics.
