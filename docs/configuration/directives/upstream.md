# `upstream`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
upstream <name> { ... }
```

| | |
|---|---|
| **Contexts** | `server` |
| **Arguments** | `name`: string |
| **Block** | required, with the directives of the `upstream` context |
| **How often** | any number of times; the first argument is a unique name (defines a `upstream`) |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

A group of backend servers that routes send requests to with the proxy directive, with load balancing and health checks.

### Security

Which servers a site can reach is an administrator decision: a route that proxies to an internal address can expose internal services.

### Example

```text
upstream "api" {
    backend "http://127.0.0.1:9001"
    backend "http://127.0.0.1:9002"
}
```
