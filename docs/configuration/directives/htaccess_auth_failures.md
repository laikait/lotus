# `htaccess_auth_failures`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
htaccess_auth_failures <count>
```

| | |
|---|---|
| **Contexts** | `site` |
| **Arguments** | `count`: integer |
| **How often** | at most once |
| **Default** | 10 |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

After this many wrong Basic authentication attempts from one client address within htaccess_auth_lockout, further attempts get 429 until the lock-out ends, without checking passwords. 0 turns the lock-out off.

### Security

Without it a password can be guessed as fast as the server answers.

### Example

```text
htaccess true
htaccess_auth_failures 5
```
