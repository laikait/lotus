# `weight`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
weight <weight>
```

| | |
|---|---|
| **Contexts** | `backend` |
| **Arguments** | `weight`: integer |
| **How often** | at most once |
| **Default** | 1 |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Share of requests relative to the other backends (round_robin, ip_hash, uri_hash, random and least_conn).

### Security

None.
