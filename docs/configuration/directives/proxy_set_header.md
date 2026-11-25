# `proxy_set_header`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
proxy_set_header <name> <value>
```

| | |
|---|---|
| **Contexts** | `route` |
| **Arguments** | `name`: string<br>`value`: string |
| **How often** | any number of times, in order |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Sets a header on the request sent to the upstream. An empty value removes the header. Forwarding headers (X-Forwarded-For, -Host, -Proto, Forwarded) are set by LOTUS and can't be changed here.

### Security

Headers such as X-Forwarded-User are trusted by many applications. Only set them to fixed values.
