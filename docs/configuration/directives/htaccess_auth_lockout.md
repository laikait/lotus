# `htaccess_auth_lockout`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
htaccess_auth_lockout <duration>
```

| | |
|---|---|
| **Contexts** | `site` |
| **Arguments** | `duration`: duration |
| **How often** | at most once |
| **Default** | 5m |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

The window in which wrong attempts are counted, and how long a client is locked out afterwards.

### Security

A long lock-out also locks out a legitimate user behind the same address.

### Example

```text
htaccess true
htaccess_auth_lockout 10m
```
