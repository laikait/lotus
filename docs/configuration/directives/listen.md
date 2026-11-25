# `listen`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
listen <name> { ... }
```

| | |
|---|---|
| **Contexts** | `server` |
| **Arguments** | `name`: string |
| **Block** | required, with the directives of the `listen` context |
| **How often** | any number of times; the first argument is a unique name (defines a `listener`) |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

A network listener. Sites attach to listeners by name.

### Security

Binding to 0.0.0.0 or [::] exposes the listener on every network interface. Ports below 1024 need special privileges (see the privilege model).
