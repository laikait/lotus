# `socket`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
socket <path>
```

| | |
|---|---|
| **Contexts** | `control` |
| **Arguments** | `path`: path |
| **How often** | at most once |
| **Default** | /run/lotus/control.sock |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Path of the control socket (named pipe name on Windows).

### Security

The socket's directory must not be writable by others; access is limited by file permissions.

### Example

```text
socket "/run/lotus/control.sock"
```
