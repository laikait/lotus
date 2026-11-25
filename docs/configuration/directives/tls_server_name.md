# `tls_server_name`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
tls_server_name <name>
```

| | |
|---|---|
| **Contexts** | `upstream` |
| **Arguments** | `name`: string |
| **How often** | at most once |
| **Default** | the backend's host |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Name to expect in the backend's certificate and to send as SNI.

### Security

None.
