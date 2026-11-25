# `balance`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
balance <method>
```

| | |
|---|---|
| **Contexts** | `upstream` |
| **Arguments** | `method`: one of round_robin|least_conn|random|ip_hash|uri_hash |
| **How often** | at most once |
| **Default** | round_robin |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

How requests are spread over the backends. round_robin follows the weights; least_conn picks the backend with the fewest active requests; random picks the less loaded of two random backends; ip_hash and uri_hash send the same client address, or the same path, to the same backend.

### Security

ip_hash and uri_hash don't balance load evenly if a few clients or paths are very busy.

### Example

```text
balance least_conn
```
