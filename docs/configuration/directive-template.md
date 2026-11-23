# Directive Documentation Template

**Spec reference:** v1.1 §19. Every directive is documented, and the documentation is generated from the directive registry.

Each directive is registered in code with the metadata below. `make docs` turns the registry into one Markdown page per directive under `docs/configuration/directives/`. CI fails if a registered directive is missing **Description**, **Security** or **Example**.

---

## Registry fields

| Field | Required | Description |
|---|---|---|
| `Name` | yes | Directive name (`max_request_body`) |
| `Contexts` | yes | Where it can appear (`server`, `site`, `route`, `match`, `limits`, …) |
| `Args` | yes | Argument types (grammar §6.7), e.g. `size` or `operator? path block` |
| `Cardinality` | yes | `single` / `repeated` / `keyed` (grammar §6.6) |
| `Default` | yes | Default value, or `none` |
| `Inherits` | yes | Whether it inherits from outer contexts |
| `Placeholders` | yes | Whether request-time `{…}` placeholders are allowed |
| `Secret` | no | Whether the value is redacted everywhere |
| `Since` | yes | First LOTUS version and config language version that support it |
| `Stability` | yes | `stable` / `experimental` / `deprecated (since, replacement)` |
| `Platforms` | no | Platform limitations (spec §2.3) |
| `Description` | yes | What it does, in plain language |
| `Security` | yes | What can go wrong if it's misconfigured, and the safe default. Write "None" only if that's true. |
| `Example` | yes | At least one valid configuration snippet (CI checks that it parses and validates) |
| `NginxEquivalent` / `ApacheEquivalent` | no | Matching directive(s) and how much of their behavior carries over |

## Generated page layout

```markdown
# `max_request_body`

| | |
|---|---|
| **Contexts** | `server` › `limits`, `site` › `limits`, `route` |
| **Arguments** | `size` |
| **Default** | `10mib` |
| **Inherits** | yes |
| **Placeholders** | no |
| **Since** | LOTUS 0.1, config v1 |
| **Stability** | stable |

## Description
Maximum size of a request body. A request whose `Content-Length` is larger
is rejected with `413` before the body is read. A chunked body is cut off
and answered with `413` as soon as it goes over the limit.

## Security
Limits memory and disk use from uploads. Setting it very high (or `0` for
unlimited) on sites that buffer request bodies lets clients exhaust disk or
memory. Raise it only on the routes that accept uploads.

## Example
    site "example.com" {
        limits { max_request_body 10mib }
        route "/upload/" { max_request_body 512mib }
    }

## Compatibility
- Nginx: `client_max_body_size` (full).
- Apache: `LimitRequestBody` (full).
```

## Writing rules

- Use the present tense and active voice. Address the reader as "you" only in examples.
- Give the HTTP status codes and log codes that the directive can produce.
- State any interaction with other directives explicitly, e.g. "overrides `limits.max_request_body` for this route".
- Never document behavior that the tests don't cover.
