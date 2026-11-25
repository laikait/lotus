# `proxy_host`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
proxy_host <host>
```

| | |
|---|---|
| **Contexts** | `route` |
| **Arguments** | `host`: one of client|upstream |
| **How often** | at most once |
| **Default** | client |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Which Host header the upstream gets: client keeps the one the client sent (and the application sees its own public name); upstream uses the backend's own address.

### Security

None.
