# `level`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
level <level>
```

| | |
|---|---|
| **Contexts** | `compression` |
| **Arguments** | `level`: one of default|fast|best |
| **How often** | at most once |
| **Default** | default |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

default balances size and CPU for responses made on the fly; fast uses the least CPU; best the most.

### Security

best costs several times the CPU of default for a few percent less data. Under load it is a way to slow the server down.

### Example

```text
level fast
```
