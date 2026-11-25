# `ca_root`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
ca_root <path>
```

| | |
|---|---|
| **Contexts** | `acme` |
| **Arguments** | `path`: path |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

PEM file with the root certificate of a private or test CA (such as Pebble), trusted only for talking to the ACME server.

### Security

Only for CAs you run yourself. It does not change which certificates browsers trust.
