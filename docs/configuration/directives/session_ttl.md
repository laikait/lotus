# `session_ttl`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
session_ttl <duration>
```

| | |
|---|---|
| **Contexts** | `admin` |
| **Arguments** | `duration`: duration |
| **How often** | at most once |
| **Default** | 8h |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

How long a sign-in lasts before the admin asks for the password again.

### Security

A short time limits what a stolen session cookie can do.

### Example

```text
address "127.0.0.1:7080"
session_ttl 2h
```
