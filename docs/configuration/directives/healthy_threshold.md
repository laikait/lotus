# `healthy_threshold`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
healthy_threshold <count>
```

| | |
|---|---|
| **Contexts** | `health_check` |
| **Arguments** | `count`: integer |
| **How often** | at most once |
| **Default** | 2 |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Successful probes in a row that bring a backend back.

### Security

None.
