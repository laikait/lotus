# `scheme`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
scheme <scheme>
```

| | |
|---|---|
| **Contexts** | `match` |
| **Arguments** | `scheme`: one of http|https |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

True if the request used this scheme.

### Security

None.

### Example

```text
scheme "http"
return 301 "https://{host}{uri}"
```
