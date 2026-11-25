# `backend`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
backend <address> [{ ... }]
```

| | |
|---|---|
| **Contexts** | `upstream` |
| **Arguments** | `address`: string |
| **Block** | optional, with the directives of the `backend` context |
| **How often** | any number of times; the first argument is a unique name |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

A server that requests are sent to: http://host:port, https://host:port or unix:/path. A block can give it a weight and a connection limit.

### Security

Requests are sent to this address as they are. Use https:// across networks you don't control, and keep tls_verify on.
