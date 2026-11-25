# `provider`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
provider <name>
```

| | |
|---|---|
| **Contexts** | `dns` |
| **Arguments** | `name`: one of cloudflare|exec |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

cloudflare sets the record through the Cloudflare API. exec runs a program you provide with `present` or `cleanup` and the record in LOTUS_ACME_ZONE, LOTUS_ACME_NAME, LOTUS_ACME_VALUE and LOTUS_ACME_TTL (see docs/architecture/acme.md).

### Security

exec runs with the privileges of the LOTUS user. Only the administrator configuration can name it.
