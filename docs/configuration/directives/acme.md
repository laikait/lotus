# `acme`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
acme { ... }
```

| | |
|---|---|
| **Contexts** | `tls` |
| **Block** | required, with the directives of the `acme` context |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Automatic certificate issuance and renewal through ACME (for example Let's Encrypt).

### Security

The account key controls certificate issuance for your domains. It is stored under the LOTUS data directory and must stay private.
