# `listeners`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
listeners <names>
```

| | |
|---|---|
| **Contexts** | `site` |
| **Arguments** | `names`: list of string |
| **How often** | at most once |
| **Default** | all listeners |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Names of the listeners this site answers on.

### Security

A site attached to a plain-HTTP listener can be read and altered on the network.
