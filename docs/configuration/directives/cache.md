# `cache`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

## In `route`

```text
cache <zone> [{ ... }]
```

| | |
|---|---|
| **Arguments** | `zone`: string |
| **Block** | optional, with the directives of the `route_cache` context |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Keeps the responses of this route's proxy or PHP in the named cache zone and answers repeated GET requests from there. Responses the backend marks private or no-store, that set cookies, or that answer requests with credentials are never kept. See the cache documentation.

### Security

A cached response is sent to every visitor who asks for the same URL. Use it only where that is what you want, and keep cookies set to bypass (the default) for pages that differ per user.

## In `server`

```text
cache <name> [{ ... }]
```

| | |
|---|---|
| **Arguments** | `name`: string |
| **Block** | optional, with the directives of the `cache` context |
| **How often** | any number of times; the first argument is a unique name (defines a `cache`) |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

A response cache: a memory budget that routes use by naming it with cache. Zones keep their entries across reloads.

### Security

A cache holds copies of responses in memory. What is stored for one visitor can be sent to another, so only routes whose responses are the same for everybody should use one.
