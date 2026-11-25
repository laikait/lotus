# `certificate`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

## In `admin`

```text
certificate <path>
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

PEM certificate chain for the admin listener. With certificate_key it makes the listener HTTPS.

### Security

None.

### Example

```text
address "0.0.0.0:7443"
certificate "/etc/lotus/admin.pem"
certificate_key "/etc/lotus/admin.key"
```

## In `site`

```text
certificate <path>
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

PEM certificate chain file for tls manual. The certificate is chosen by SNI when the site name or an alias matches.

### Security

The file is read once at start and on reload.

### Example

```text
tls manual
certificate "/etc/ssl/example.pem"
certificate_key "/etc/ssl/example.key"
```
