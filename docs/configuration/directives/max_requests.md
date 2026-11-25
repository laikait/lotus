# `max_requests`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
max_requests <count>
```

| | |
|---|---|
| **Contexts** | `php_backend` |
| **Arguments** | `count`: integer |
| **How often** | at most once |
| **Default** | 500 |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

A php-cgi process exits and is replaced after this many requests, which contains memory leaks. 0 means never.

### Security

Applies to command.
