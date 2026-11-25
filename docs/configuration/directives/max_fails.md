# `max_fails`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
max_fails <count>
```

| | |
|---|---|
| **Contexts** | `upstream` |
| **Arguments** | `count`: integer |
| **How often** | at most once |
| **Default** | 3 |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

After this many failures within fail_timeout a backend is taken out of rotation for fail_timeout. 0 turns passive checking off.

### Security

None.
