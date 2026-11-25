# `tls_client_cert`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
tls_client_cert <path>
```

| | |
|---|---|
| **Contexts** | `upstream` |
| **Arguments** | `path`: path |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Certificate that LOTUS presents to https backends that require client certificates (with tls_client_key).

### Security

The key file must be readable only by the LOTUS user.

### Example

```text
tls_client_cert "/etc/lotus/client.pem"
tls_client_key "/etc/lotus/client.key"
```
