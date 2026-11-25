# `root`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
root <path>
```

| | |
|---|---|
| **Contexts** | `route`, `site` |
| **Arguments** | `path`: path |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Document root: the directory that URLs are mapped into.

### Security

Everything below this directory can be served. Pointing it at a directory with secrets exposes them; LOTUS still refuses to follow paths outside it according to the symlink policy.
