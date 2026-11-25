# `http3`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
http3 <enabled>
```

| | |
|---|---|
| **Contexts** | `listen` |
| **Arguments** | `enabled`: bool |
| **How often** | at most once |
| **Default** | false |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Also serve HTTP/3 over UDP on the same port number and advertise it with Alt-Svc. Requires protocol https.

### Security

Needs the UDP port open in the firewall. QUIC has its own limits and tuning needs (see operations docs).

### Example

```text
protocol https
http3 true
```
