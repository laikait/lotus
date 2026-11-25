# `http_port`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
http_port <port>
```

| | |
|---|---|
| **Contexts** | `acme` |
| **Arguments** | `port`: integer |
| **How often** | at most once |
| **Default** | 80 |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Port the CA connects to for HTTP-01 validation, if it is not 80 (for example behind a port forward). A plain-HTTP listener must answer on it.

### Security

None.
