# `users_file`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
users_file <path>
```

| | |
|---|---|
| **Contexts** | `admin` |
| **Arguments** | `path`: path |
| **How often** | at most once |
| **Default** | admin-users.txt, next to the main configuration file |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

File with the admin accounts (name, role and password hash). Made and changed with `lotus admin passwd`.

### Security

The file holds password hashes, not passwords; keep it readable by the LOTUS service only.

### Example

```text
address "127.0.0.1:7080"
users_file "/etc/lotus/admin-users.txt"
```
