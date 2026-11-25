# `try_files`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
try_files <candidates>
```

| | |
|---|---|
| **Contexts** | `match`, `route` |
| **Arguments** | `candidates`: list of string |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | yes |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Tries each candidate as a file or directory in the site root. If none exists, the last entry is handled as an internal redirect (or "=404" returns that status).

### Security

Candidates are resolved inside the document root only. The last entry re-enters routing, so it can reach any route of the site.
