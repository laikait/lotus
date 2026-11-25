# `tls_verify`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
tls_verify <enabled>
```

| | |
|---|---|
| **Contexts** | `upstream` |
| **Arguments** | `enabled`: bool |
| **How often** | at most once |
| **Default** | true |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Check the certificate of https backends against the trusted authorities and the backend's name.

### Security

false lets anyone between LOTUS and the backend read and change the traffic. Use tls_ca for a private authority instead.

### Example

```text
tls_verify true
```
