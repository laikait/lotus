# `htaccess_unsupported`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
htaccess_unsupported <action>
```

| | |
|---|---|
| **Contexts** | `site` |
| **Arguments** | `action`: one of error|ignore |
| **How often** | at most once |
| **Default** | error |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

What happens when a .htaccess file holds a directive LOTUS doesn't support or the site doesn't allow: error answers 500 and logs the directive; ignore skips it with a warning. Directives that grant or restrict access are never skipped.

### Security

ignore can change what a site does without any visible failure. Use `lotus compat htaccess check` before relying on it.

### Example

```text
htaccess true
htaccess_unsupported ignore
```
