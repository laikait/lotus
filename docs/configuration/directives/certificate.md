# `certificate`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
certificate <path>
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

PEM certificate chain file for tls manual. The certificate is chosen by SNI when the site name or an alias matches.

### Security

The file is read once at start and on reload.
