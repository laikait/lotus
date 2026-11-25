# `certificate_key`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
certificate_key <path>
```

| | |
|---|---|
| **Contexts** | `site` |
| **Arguments** | `path`: path |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

PEM private key file that belongs to certificate.

### Security

The key must be readable only by the user that starts LOTUS (before it drops privileges). Anyone who can read it can impersonate the site.

### Example

```text
tls manual
certificate "/etc/ssl/example.pem"
certificate_key "/etc/ssl/example.key"
```
