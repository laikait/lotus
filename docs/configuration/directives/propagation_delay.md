# `propagation_delay`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
propagation_delay <duration>
```

| | |
|---|---|
| **Contexts** | `dns` |
| **Arguments** | `duration`: duration |
| **How often** | at most once |
| **Default** | 0s |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Time to wait after the record is published before checking that it is visible.

### Security

None.
