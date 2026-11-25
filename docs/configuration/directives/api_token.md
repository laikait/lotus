# `api_token`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
api_token <token>
```

| | |
|---|---|
| **Contexts** | `dns` |
| **Arguments** | `token`: string |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Secret** | yes: redacted in every output |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Cloudflare API token with Zone.DNS:Edit (and Zone:Read unless limited to one zone). Not a global API key.

### Security

A credential: it is redacted in all output. Prefer env("NAME") over writing it into the file.
