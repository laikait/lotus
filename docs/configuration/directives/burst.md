# `burst`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
burst <count>
```

| | |
|---|---|
| **Contexts** | `rate_limit` |
| **Arguments** | `count`: integer |
| **How often** | at most once |
| **Default** | requests |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

How many requests may arrive at once after a quiet time. The allowance refills continuously at requests per period.

### Security

A small burst smooths traffic but refuses pages that load many files at once.

### Example

```text
requests 5
per 1s
burst 10
```
