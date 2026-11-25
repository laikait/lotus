# `symlinks`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
symlinks <policy>
```

| | |
|---|---|
| **Contexts** | `site` |
| **Arguments** | `policy`: one of follow|owner_match|deny |
| **How often** | at most once |
| **Default** | owner_match |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

How symbolic links below the document root are treated: deny refuses them, owner_match follows a link only if the link and its target have the same owner, follow allows them. A link that leads outside the document root is always refused, and so is every absolute link, even one that points back inside; use relative links.

### Security

On shared hosts keep owner_match or deny so one tenant can not link to another tenant's files.

### Example

```text
symlinks deny
```
