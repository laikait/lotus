# `min_version`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
min_version <version>
```

| | |
|---|---|
| **Contexts** | `tls` |
| **Arguments** | `version`: one of 1.2|1.3 |
| **How often** | at most once |
| **Default** | 1.2 |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Lowest TLS version accepted. TLS 1.0 and 1.1 are not supported.

### Security

Raising it to 1.3 blocks older clients but removes legacy cipher suites.
