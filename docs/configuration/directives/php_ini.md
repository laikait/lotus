# `php_ini`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
php_ini <path>
```

| | |
|---|---|
| **Contexts** | `php_backend` |
| **Arguments** | `path`: path |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

php.ini file for the processes started with command (php-cgi -c). Without it PHP searches its usual places.

### Security

The file decides which PHP settings apply, including open_basedir and disabled functions.
