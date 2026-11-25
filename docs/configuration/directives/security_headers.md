# `security_headers`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
security_headers { ... }
```

| | |
|---|---|
| **Contexts** | `site` |
| **Block** | required, with the directives of the `security_headers` context |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Response headers added to every response, written as "Name" = "value" entries.

### Security

Headers such as Content-Security-Policy and Strict-Transport-Security harden browsers against attacks. Values can't contain control characters, so they can't inject extra headers.

### Example

```text
security_headers {
    "X-Content-Type-Options" = "nosniff"
    "X-Frame-Options" = "DENY"
}
```
