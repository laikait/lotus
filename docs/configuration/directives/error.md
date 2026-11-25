# `error`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
error <path>
```

| | |
|---|---|
| **Contexts** | `logs` |
| **Arguments** | `path`: path or null |
| **How often** | at most once |
| **Default** | server error log |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

File that receives this site's error log. null turns it off.

### Security

Error logs can contain request details.
