# `max_conns`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

## In `backend`

```text
max_conns <count>
```

| | |
|---|---|
| **Arguments** | `count`: integer |
| **How often** | at most once |
| **Default** | unlimited |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Most requests in progress on this backend. Requests go to other backends while it is full.

### Security

None.

## In `upstream`

```text
max_conns <count>
```

| | |
|---|---|
| **Arguments** | `count`: integer |
| **How often** | at most once |
| **Default** | unlimited |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Most requests in progress across the whole upstream. Beyond it requests get 503 at once.

### Security

Protects the backends from a flood; set it to what they can serve.
