# `max_procs`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
max_procs <count>
```

| | |
|---|---|
| **Contexts** | `runtime` |
| **Arguments** | `count`: integer or one of auto |
| **How often** | at most once |
| **Default** | auto |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Maximum number of CPUs Go uses (GOMAXPROCS). auto follows the CPU count and container limits.

### Security

None.

### Example

```text
max_procs 4
```
