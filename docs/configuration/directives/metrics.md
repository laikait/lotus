# `metrics`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
metrics { ... }
```

| | |
|---|---|
| **Contexts** | `server` |
| **Block** | required, with the directives of the `metrics` context |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

A separate listener that serves metrics in the Prometheus text format (requests, latency, connections, backends, certificates). Off unless this block is present.

### Security

Metrics tell an attacker what sites exist and how busy they are. Keep the listener on a loopback or private address, and require a bearer_token or an allow list if it is reachable from a network.
