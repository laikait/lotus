# `php`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
php <backend>
```

| | |
|---|---|
| **Contexts** | `match`, `route` |
| **Arguments** | `backend`: string |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Runs matching requests through the named php_backend over FastCGI.

### Security

Only existing files below the document root are ever passed to PHP. Which backend (and so which pool and OS user) runs a site is an administrator decision.
