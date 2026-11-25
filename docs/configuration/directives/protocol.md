# `protocol`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
protocol <protocol>
```

| | |
|---|---|
| **Contexts** | `listen` |
| **Arguments** | `protocol`: one of http|https |
| **How often** | at most once |
| **Default** | http |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

http serves plain HTTP; https serves TLS (HTTP/1.1 and HTTP/2).

### Security

Plain HTTP is not encrypted. Redirect it to HTTPS or use it only on trusted networks.

### Example

```text
protocol https
```
