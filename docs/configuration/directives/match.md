# `match`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
match { ... }
```

| | |
|---|---|
| **Contexts** | `route` |
| **Block** | required, with the directives of the `match` context |
| **How often** | any number of times, in order |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Applies different actions when extra conditions hold. The first match block whose conditions all hold wins; its actions replace the route's own.

### Security

Conditions on client_ip use the resolved client IP, which depends on trusted_proxies being correct.
