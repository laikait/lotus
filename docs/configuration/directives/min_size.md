# `min_size`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
min_size <size>
```

| | |
|---|---|
| **Contexts** | `compression` |
| **Arguments** | `size`: size |
| **How often** | at most once |
| **Default** | 1kib |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Smallest body that is compressed. Smaller ones are sent as they are. A response of unknown length is held back until it reaches this size or ends.

### Security

A very small value compresses responses that don't shrink and holds back streams.
