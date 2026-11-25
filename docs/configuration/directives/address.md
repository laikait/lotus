# `address`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

## In `listen`

```text
address <address>
```

| | |
|---|---|
| **Arguments** | `address`: address (host:port) |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Address to listen on, as host:port, [ipv6]:port or :port.

### Security

0.0.0.0 and [::] accept connections from every interface.

### Example

```text
address "0.0.0.0:8081"
```

## In `metrics`

```text
address <address>
```

| | |
|---|---|
| **Arguments** | `address`: address (host:port) |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Address to listen on, as host:port. Use 127.0.0.1 unless a collector on another machine must reach it.

### Security

A non-loopback address needs bearer_token or allow.

### Example

```text
address "127.0.0.1:9100"
```
