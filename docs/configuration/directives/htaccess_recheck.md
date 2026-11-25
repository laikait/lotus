# `htaccess_recheck`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
htaccess_recheck <interval>
```

| | |
|---|---|
| **Contexts** | `site` |
| **Arguments** | `interval`: duration |
| **How often** | at most once |
| **Default** | 2s |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

How long a .htaccess file (or its absence) is remembered before the file system is looked at again. 0s checks on every request.

### Security

Short intervals cost a file system lookup per directory per request.
