# `control`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
control { ... }
```

| | |
|---|---|
| **Contexts** | `server` |
| **Block** | required, with the directives of the `control` context |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Settings for the local control channel used by the lotus command.

### Security

Anyone who can connect to the control channel can reload or stop the server.

### Example

```text
control {
    socket "/run/lotus/control.sock"
}
```
