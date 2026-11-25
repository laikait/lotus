# `php_deny`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
php_deny <enabled>
```

| | |
|---|---|
| **Contexts** | `match`, `route` |
| **Arguments** | `enabled`: bool |
| **How often** | at most once |
| **Default** | false |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

When true, PHP files under this route are never executed, even if a PHP backend is configured.

### Security

Use it on upload directories so a file uploaded by a visitor can't be run as code.
