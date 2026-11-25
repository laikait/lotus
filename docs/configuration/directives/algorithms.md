# `algorithms`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
algorithms <codings>
```

| | |
|---|---|
| **Contexts** | `compression` |
| **Arguments** | `codings`: list of one of br|zstd|gzip |
| **How often** | at most once |
| **Default** | [ "br", "zstd", "gzip" ] |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

The codings LOTUS may use, most preferred first. A client's own weights win; this order breaks ties. Brotli (br) shrinks most, Zstandard (zstd) is fastest at a similar size, gzip works everywhere.

### Security

None.
