# `rewrite`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
rewrite <pattern> <target> [<flag>]
```

| | |
|---|---|
| **Contexts** | `route` |
| **Arguments** | `pattern`: regular expression<br>`target`: string<br>`flag`: one of last|redirect|permanent (optional) |
| **How often** | any number of times, in order |
| **Default** | none |
| **Request-time placeholders** | yes |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

If the request path matches the pattern, replaces the request: with last (the default) routing starts again with the new URI, redirect answers 302 and permanent answers 301. Rewrites are tried in order and the first match wins; they run before return and try_files. In the target, {re.1}, {re.2}... are the pattern's capture groups.

### Security

A redirect target built from {re.N} can become an open redirect if the pattern lets a visitor choose the host, as in '^/go/(.*)$' to "{re.1}". Start the target with "/" or a fixed host. CR/LF characters in captured values are rejected.

### Example

```text
rewrite '^/blog/(\d+)$' "/posts/{re.1}.html"
```
