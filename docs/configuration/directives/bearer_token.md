# `bearer_token`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
bearer_token <token>
```

| | |
|---|---|
| **Contexts** | `metrics` |
| **Arguments** | `token`: string |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Secret** | yes: redacted in every output |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

If set, a request must carry `Authorization: Bearer TOKEN`. Compared in constant time.

### Security

A credential: it is redacted in all output. Prefer env("NAME") over writing it into the file.

### Example

```text
address "127.0.0.1:9100"
bearer_token env("METRICS_TOKEN")
```
