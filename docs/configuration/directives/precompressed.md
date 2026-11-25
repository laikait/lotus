# `precompressed`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
precompressed <enabled>
```

| | |
|---|---|
| **Contexts** | `compression` |
| **Arguments** | `enabled`: bool |
| **How often** | at most once |
| **Default** | false |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

For files served from disk, send file.css.br, file.css.zst or file.css.gz (made beforehand, one per algorithm) in place of file.css when the client accepts that coding, with no CPU spent per request. A copy older than its original is ignored.

### Security

The copies are read under the same rules as every file (symlinks, dotfiles). Keep them in step with the originals: a stale copy is never served, but a wrong one is.
