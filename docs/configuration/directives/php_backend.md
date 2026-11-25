# `php_backend`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
php_backend <name> { ... }
```

| | |
|---|---|
| **Contexts** | `server` |
| **Arguments** | `name`: string |
| **Block** | required, with the directives of the `php_backend` context |
| **How often** | any number of times; the first argument is a unique name (defines a `php_backend`) |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

A FastCGI backend (for example a PHP-FPM pool). Routes use it with the php directive.

### Security

Use one pool, with its own OS user, per site. Sites that share a pool can read each other's files and sessions.
