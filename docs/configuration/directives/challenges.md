# `challenges`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
challenges <types>
```

| | |
|---|---|
| **Contexts** | `acme` |
| **Arguments** | `types`: list of one of http-01|tls-alpn-01|dns-01 |
| **How often** | at most once |
| **Default** | [ "http-01", "tls-alpn-01" ] (plus "dns-01" with a dns block) |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Which validations LOTUS offers the CA. http-01 needs the CA to reach a plain-HTTP listener on port 80; tls-alpn-01 needs an https listener on port 443; dns-01 needs a dns block and is the only one that works for wildcard names. Use one when the other port is closed.

### Security

None.
