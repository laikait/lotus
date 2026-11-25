# `max_entry_size`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
max_entry_size <size>
```

| | |
|---|---|
| **Contexts** | `cache` |
| **Arguments** | `size`: size |
| **How often** | at most once |
| **Default** | 1mib |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Largest response body that is stored. Larger responses are passed on and not kept. Must not exceed max_size.

### Security

Large entries squeeze out many small ones; keep it small unless the routes serve big files that are worth keeping.
