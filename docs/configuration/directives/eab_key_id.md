# `eab_key_id`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
eab_key_id <id>
```

| | |
|---|---|
| **Contexts** | `acme` |
| **Arguments** | `id`: string |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

External account binding key ID, for CAs that require it.

### Security

Treat as a credential.

### Example

```text
eab_key_id "kid-1"
```
