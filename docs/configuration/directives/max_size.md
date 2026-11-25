# `max_size`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
max_size <size>
```

| | |
|---|---|
| **Contexts** | `cache` |
| **Arguments** | `size`: size |
| **How often** | at most once |
| **Default** | 64mib |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Most memory the zone's stored responses (bodies, headers and bookkeeping) may use. When it is full the least recently used entries are dropped. Responses being copied while they stream also count against it.

### Security

Memory the cache uses is not available to the rest of the system. Keep it well below the machine's memory (see runtime memory_limit).
