# `header`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
header <name> [<operator>] <value>
```

| | |
|---|---|
| **Contexts** | `match` |
| **Arguments** | `name`: string<br>`operator`: operator (= ~ ~* !~ !~*) (optional)<br>`value`: string |
| **How often** | any number of times, in order |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Tests a request header.

### Security

Values come from the client and can be forged; don't use them alone for access control.
