# `limits`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
limits { ... }
```

| | |
|---|---|
| **Contexts** | `server`, `site` |
| **Block** | required, with the directives of the `limits` context |
| **How often** | at most once |
| **Default** | see the directives inside |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Connection, size and timeout limits. In a site, they override the server-wide values one by one.

### Security

These limits protect against resource exhaustion (slowloris, huge uploads). Raising them weakens that protection, so only administrators may change them.

### Example

In `server`:

```text
limits {
    max_connections 20000
    max_request_body 25mib
}
```

In `site`:

```text
limits {
    max_request_body 50mib
}
```

