# `storage`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
storage <path>
```

| | |
|---|---|
| **Contexts** | `acme` |
| **Arguments** | `path`: path |
| **How often** | at most once |
| **Default** | /var/lib/lotus/certs (Windows: %ProgramData%\lotus\certs) |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Directory for the ACME account key, certificates and their private keys. Created with access for the LOTUS user only.

### Security

Everything in it is secret. Anyone who can read it can impersonate your sites. Back it up: lost certificates are re-issued, but CAs limit how often.
