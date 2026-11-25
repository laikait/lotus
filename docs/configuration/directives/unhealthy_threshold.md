# `unhealthy_threshold`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
unhealthy_threshold <count>
```

| | |
|---|---|
| **Contexts** | `health_check` |
| **Arguments** | `count`: integer |
| **How often** | at most once |
| **Default** | 3 |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Failed probes in a row that take a backend out.

### Security

None.

### Example

```text
unhealthy_threshold 2
```
