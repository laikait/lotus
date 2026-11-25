# `viewer_group`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
viewer_group <group>
```

| | |
|---|---|
| **Contexts** | `control` |
| **Arguments** | `group`: string |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

OS group whose members get read-only access to the control channel.

### Security

Members can read status and configuration (secrets are redacted) but can't change anything.

### Example

```text
viewer_group "lotus-viewers"
```
