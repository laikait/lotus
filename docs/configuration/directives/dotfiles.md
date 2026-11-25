# `dotfiles`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
dotfiles <policy>
```

| | |
|---|---|
| **Contexts** | `site` |
| **Arguments** | `policy`: one of deny|allow |
| **How often** | at most once |
| **Default** | deny |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Whether files and directories whose names start with a dot (such as .git or .env) can be served. /.well-known/ is always allowed.

### Security

Dotfiles often hold secrets and repository data. Allow them only if you are sure.

### Example

```text
dotfiles deny
```
