# `interval`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
interval <duration>
```

| | |
|---|---|
| **Contexts** | `health_check` |
| **Arguments** | `duration`: duration |
| **How often** | at most once |
| **Default** | 10s |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Time between probes of a backend.

### Security

Short intervals add load to the backends.
