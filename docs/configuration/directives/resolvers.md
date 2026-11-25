# `resolvers`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
resolvers <servers>
```

| | |
|---|---|
| **Contexts** | `dns` |
| **Arguments** | `servers`: list of string |
| **How often** | at most once |
| **Default** | the system's |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

DNS servers (host:port) asked whether the record is visible and which zone a name belongs to.

### Security

None.
