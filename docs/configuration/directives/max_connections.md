# `max_connections`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
max_connections <count>
```

| | |
|---|---|
| **Contexts** | `limits` |
| **Arguments** | `count`: integer |
| **How often** | at most once |
| **Default** | 10000 |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Maximum number of simultaneous connections. New connections beyond it are closed.

### Security

Too high a value lets clients exhaust file descriptors and memory.

### Example

```text
max_connections 20000
```
