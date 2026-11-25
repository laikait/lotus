# `max_connections_per_ip`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
max_connections_per_ip <count>
```

| | |
|---|---|
| **Contexts** | `limits` |
| **Arguments** | `count`: integer |
| **How often** | at most once |
| **Default** | unlimited |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Maximum simultaneous connections from one client address (IPv6 clients are counted per /64). Connections beyond it are closed. Connections from trusted_proxies are not counted, since every client of such a proxy shares its address. Server-wide only.

### Security

Without it one client can use every connection slot. Too low a value affects many users behind one NAT address.

### Example

```text
max_connections_per_ip 50
```
