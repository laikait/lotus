# `memory_limit`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
memory_limit <size>
```

| | |
|---|---|
| **Contexts** | `runtime` |
| **Arguments** | `size`: size |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Soft memory limit for the Go runtime (GOMEMLIMIT). The garbage collector works harder as usage approaches it.

### Security

Set it below the container or machine limit so the process is not killed by the OS.

### Example

```text
memory_limit 2gib
```
