# `retries`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
retries <count>
```

| | |
|---|---|
| **Contexts** | `upstream` |
| **Arguments** | `count`: integer |
| **How often** | at most once |
| **Default** | 1 |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

How many other backends a request may be tried on after a failure. Only requests that are safe to repeat are: those that never connected, and GET, HEAD, OPTIONS, PUT and DELETE without a body that failed before a response.

### Security

A POST that reached a backend is never repeated, so this can't make a payment twice.
