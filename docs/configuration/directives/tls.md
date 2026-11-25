# `tls`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

## In `server`

```text
tls { ... }
```

| | |
|---|---|
| **Block** | required, with the directives of the `tls` context |
| **How often** | at most once |
| **Default** | see the directives inside |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Server-wide TLS settings.

### Security

Weak TLS settings expose traffic to downgrade and decryption attacks.

### Example

```text
tls {
    min_version "1.2"
}
```

## In `site`

```text
tls <mode>
```

| | |
|---|---|
| **Arguments** | `mode`: one of automatic|manual|off |
| **How often** | at most once |
| **Default** | automatic when the listener is https |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

automatic obtains certificates through ACME; manual uses certificates you provide; off serves no TLS for this site.

### Security

With off, this site is served without encryption even on an https listener where possible.

### Example

```text
tls off
```
