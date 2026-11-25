# `enabled`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
enabled <enabled>
```

| | |
|---|---|
| **Contexts** | `compression` |
| **Arguments** | `enabled`: bool |
| **How often** | at most once |
| **Default** | true |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

With false, compression is off (in a site: for this site even if the server's block turns it on).

### Security

None.

### Example

```text
enabled false
```
