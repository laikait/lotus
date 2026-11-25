# `types`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
types <media_types>
```

| | |
|---|---|
| **Contexts** | `compression` |
| **Arguments** | `media_types`: list of string |
| **How often** | at most once |
| **Default** | text/*, application/json, application/javascript, application/xml, image/svg+xml, fonts in TTF and OTF, ... |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

The media types that are compressed: exact ("application/json") or a whole family ("text/*"). A response with another type, or none, is sent as it is.

### Security

Don't add types that are already compressed (images, video, archives, WOFF fonts): it costs CPU for nothing, and compressing responses that mix secrets with reflected input enables BREACH-style attacks.
