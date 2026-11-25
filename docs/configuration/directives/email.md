# `email`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
email <address>
```

| | |
|---|---|
| **Contexts** | `acme` |
| **Arguments** | `address`: string |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Contact email registered with the certificate authority.

### Security

Used by the CA for expiry and problem notices.
