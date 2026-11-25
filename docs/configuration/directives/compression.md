# `compression`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
compression [{ ... }]
```

| | |
|---|---|
| **Contexts** | `server`, `site` |
| **Block** | optional, with the directives of the `compression` context |
| **How often** | at most once |
| **Default** | off |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Compresses responses (Brotli, Zstandard, gzip) for clients that accept it. Off unless a compression block is present; in a site it overrides the server's block setting by setting. Static files can also be served from precompressed copies.

### Security

Compressing a page that mixes a secret (a CSRF token) with text an attacker can make the page repeat can leak the secret to an attacker who watches the sizes (BREACH). Compress only types you need, and keep secrets out of pages that reflect input. Compression also costs CPU; LOTUS stops compressing when too many responses are being compressed at once.
