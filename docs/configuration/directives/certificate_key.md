# `certificate_key`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

## In `admin`

```text
certificate_key <path>
```

| | |
|---|---|
| **Arguments** | `path`: path |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

PEM private key for the admin listener.

### Security

The key must be readable only by the LOTUS service.

### Example

```text
address "0.0.0.0:7443"
certificate "/etc/lotus/admin.pem"
certificate_key "/etc/lotus/admin.key"
```

## In `site`

```text
certificate_key <path>
```

| | |
|---|---|
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
