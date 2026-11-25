# `ca`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
ca <url>
```

| | |
|---|---|
| **Contexts** | `acme` |
| **Arguments** | `url`: string |
| **How often** | at most once |
| **Default** | Let's Encrypt production |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

ACME directory URL of the certificate authority.

### Security

Use a staging CA while testing to avoid production rate limits.

### Example

```text
ca "https://acme-staging-v02.api.letsencrypt.org/directory"
```
