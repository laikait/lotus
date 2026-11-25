# `proxy`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
proxy <upstream>
```

| | |
|---|---|
| **Contexts** | `match`, `route` |
| **Arguments** | `upstream`: string |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Sends matching requests to the named upstream (a reverse proxy). Takes the place of try_files and php for the route.

### Security

The request, with its headers and body, goes to a server of that upstream. Only administrators may choose upstreams.
