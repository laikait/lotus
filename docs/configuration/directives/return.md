# `return`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
return <status> [<target>]
```

| | |
|---|---|
| **Contexts** | `match`, `route` |
| **Arguments** | `status`: integer<br>`target`: string (optional) |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | yes |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Answer immediately with a status code. For redirect statuses (301, 302, 303, 307, 308) the second argument is the target URL; otherwise it is the response body.

### Security

A redirect target built from {placeholders} can become an open redirect if it uses a client-controlled value such as {query.next}. CR/LF characters in placeholder values are rejected.
