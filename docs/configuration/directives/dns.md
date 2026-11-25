# `dns`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
dns { ... }
```

| | |
|---|---|
| **Contexts** | `acme` |
| **Block** | required, with the directives of the `dns` context |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

How LOTUS publishes the DNS record for a dns-01 validation. Enables dns-01 and wildcard certificates.

### Security

The credentials can change the DNS of your domains. Give them the least access that works (one zone, DNS records only).
