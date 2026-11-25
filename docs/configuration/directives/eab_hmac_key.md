# `eab_hmac_key`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
eab_hmac_key <key>
```

| | |
|---|---|
| **Contexts** | `acme` |
| **Arguments** | `key`: string |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Secret** | yes: redacted in every output |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

External account binding HMAC key, for CAs that require it.

### Security

A credential: it is redacted in all output. Prefer env("NAME") over writing it into the file.
