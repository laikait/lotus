# `route`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
route [<operator>] <path> { ... }
```

| | |
|---|---|
| **Contexts** | `route`, `site` |
| **Arguments** | `operator`: operator (= ^~ ~ ~*) (optional)<br>`path`: string |
| **Block** | required, with the directives of the `route` context |
| **How often** | any number of times, in order |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Handles requests whose path matches. See the routing precedence document for how routes are chosen.

### Security

A regular-expression route is tried before shorter prefix routes. A broad pattern can capture requests meant for a protected prefix; mark protected prefixes with ^~.

### Example

In `route`:

```text
route ~ '\.json$' {
    return 200 "{}"
}
```

In `site`:

```text
route "/blog/" {
    try_files [ "{uri}", "{uri}/", "=404" ]
}
```

