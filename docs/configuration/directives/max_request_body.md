# `max_request_body`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
max_request_body <size>
```

| | |
|---|---|
| **Contexts** | `limits`, `route` |
| **Arguments** | `size`: size |
| **How often** | at most once |
| **Default** | 10mib |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Largest request body accepted. Bigger bodies get 413 before they are read.

### Security

Limits memory and disk use from uploads. Raise it only for the routes that need it.
