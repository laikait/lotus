# `requests`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
requests <count>
```

| | |
|---|---|
| **Contexts** | `rate_limit` |
| **Arguments** | `count`: integer |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

How many requests are allowed per period (see per).

### Security

Set it above what a real visitor does, so only abusive clients reach it.

### Example

```text
requests 10
per 1s
```
