# `client_ip`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
client_ip [<operator>] <networks>
```

| | |
|---|---|
| **Contexts** | `match` |
| **Arguments** | `operator`: operator (= !~) (optional)<br>`networks`: list of IP address or CIDR |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

True if the client IP is in one of the networks (with !~: if it is in none of them).

### Security

Behind a proxy the client IP is only correct when trusted_proxies lists that proxy.
