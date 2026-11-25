# `admin`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
admin { ... }
```

| | |
|---|---|
| **Contexts** | `server` |
| **Block** | required, with the directives of the `admin` context |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

The web admin: a login page and a dashboard to manage the server, on a listener of its own. Off unless this block is present.

### Security

Whoever signs in to the admin can change what the server does. Keep it on a loopback address, or give it a certificate and an allow list. Accounts are made with `lotus admin passwd`.

### Example

```text
admin {
    address "127.0.0.1:7080"
}
```
