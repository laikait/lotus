# `tls_ca`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
tls_ca <path>
```

| | |
|---|---|
| **Contexts** | `upstream` |
| **Arguments** | `path`: path |
| **How often** | at most once |
| **Default** | system certificates |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

PEM file of the authority that signed the backends' certificates. It replaces the system list for this upstream.

### Security

None.

### Example

```text
tls_ca "/etc/ssl/internal-ca.pem"
```
