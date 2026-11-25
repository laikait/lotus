# `propagation_timeout`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
propagation_timeout <duration>
```

| | |
|---|---|
| **Contexts** | `dns` |
| **Arguments** | `duration`: duration |
| **How often** | at most once |
| **Default** | 2m |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

How long to wait for the record to become visible before the CA is asked to check. 0 uses the default.

### Security

None.
