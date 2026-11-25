# `health_check`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
health_check { ... }
```

| | |
|---|---|
| **Contexts** | `upstream` |
| **Block** | required, with the directives of the `health_check` context |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Probes every backend regularly and takes failing ones out of rotation until they recover. Without a path it only tries to connect.

### Security

A health path is requested without credentials. Don't point it at something that changes data.
