# `method`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
method <methods>
```

| | |
|---|---|
| **Contexts** | `match` |
| **Arguments** | `methods`: list of one of GET|HEAD|POST|PUT|DELETE|PATCH|OPTIONS |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

True if the request method is one of these.

### Security

None.

### Example

```text
method [ "POST" ]
return 405
```
