# `aliases`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
aliases <hostnames>
```

| | |
|---|---|
| **Contexts** | `site` |
| **Arguments** | `hostnames`: list of hostname |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Additional hostnames served by this site. Wildcards like *.example.com are allowed.

### Security

Whoever controls the aliases controls which hostnames reach this site, so tenants must not edit them.
