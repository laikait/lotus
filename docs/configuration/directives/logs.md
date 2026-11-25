# `logs`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
logs { ... }
```

| | |
|---|---|
| **Contexts** | `site` |
| **Block** | required, with the directives of the `logs` context |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Per-site log files.

### Security

LOTUS writes these files as its own user. A site's log path must not point at files it should not be able to modify.
