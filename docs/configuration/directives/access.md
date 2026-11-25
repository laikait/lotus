# `access`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
access <path>
```

| | |
|---|---|
| **Contexts** | `logs` |
| **Arguments** | `path`: path or null |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

File that receives the access log. null turns it off.

### Security

The file is opened as the LOTUS user; access logs contain client IPs and URLs (personal data).

### Example

```text
access "/var/log/lotus/example.access.log"
```
