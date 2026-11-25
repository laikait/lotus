# `propagation_check`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
propagation_check <enabled>
```

| | |
|---|---|
| **Contexts** | `dns` |
| **Arguments** | `enabled`: bool |
| **How often** | at most once |
| **Default** | true |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Check, before asking the CA to validate, that the record can be seen in DNS. Turn it off when the name servers you can query don't show what the CA sees (split-horizon DNS).

### Security

Off means the CA may be asked too early; the attempt then fails and is retried later.
