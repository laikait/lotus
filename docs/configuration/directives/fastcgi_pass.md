# `fastcgi_pass`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
fastcgi_pass <address>
```

| | |
|---|---|
| **Contexts** | `php_backend` |
| **Arguments** | `address`: address (host:port or unix:/path) |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

FastCGI server address: unix:/path/to/socket or host:port. For a server that runs on its own, such as PHP-FPM.

### Security

Prefer Unix sockets with restricted permissions. A TCP address that is reachable by others lets them run PHP as this pool.

### Example

```text
fastcgi_pass "unix:/run/php/php-fpm.sock"
```
