# Route Matching Precedence

**Status:** implemented in `internal/router` and `internal/server/route.go` (path matching §3, `match` blocks §4, `try_files`, `rewrite`, `error_page` and the 10-redirect limit §5). The Nginx translator is `lotus compat nginx convert` (`docs/architecture/nginx-compat.md`). Not implemented yet: a site-level `rewrite` (it is a route directive for now), the Nginx differential tests (§8). `client_ip` uses the address resolved by `internal/clientip` (spec §4.10.4); PROXY protocol is not implemented yet.
**Spec reference:** v1.1 §4.3.3 (decision confirmed: Nginx `location` model)
**Syntax reference:** `docs/configuration/grammar.md` §6.4

This document defines how LOTUS picks the route for a request. The router and the Nginx translator must both follow it exactly, and the conformance tests in §8 check it against Nginx.

---

## 1. Input

Routing runs **after** the virtual host is chosen. It uses the **normalized path** from spec §4.3.4: percent-decoded unreserved characters, `.`/`..` resolved, repeated slashes merged (if enabled), no query string.

Paths are compared byte by byte and case-sensitively, except for `~*` regex routes.

## 2. Route kinds

| Kind | Syntax | Can contain child routes | Can contain `match` |
|---|---|---|---|
| Exact | `route = "/p"` | No | Yes |
| Prefix | `route "/p"` | Yes | Yes |
| Final prefix | `route ^~ "/p"` | Yes | Yes |
| Regex | `route ~ '<re>'` / `route ~* '<re>'` | No | Yes |
| Named | `route "@name"` | No | Yes |

Validation rules:

- A child route's path (exact/prefix) must start with its parent's prefix. Child regex routes are allowed, but only run when their parent is the chosen prefix (§3).
- Two exact routes, or two prefix routes, with the same path in the same parent → error `LOTUS-CFG-0301`.
- A prefix and a final prefix with the same path → error.
- A regex that can't compile under RE2 → error.
- Warning `LOTUS-CFG-W031`: a prefix that doesn't end in `/` (other than `"/"`) and is also a prefix of an existing sibling path. Example: `/doc` also matches `/docs`.
- Warning `LOTUS-CFG-W032`: a regex route that comes after another regex route that matches everything it would match (detected with a sample-string heuristic; best effort).

## 3. Algorithm

`find(routes, path)` searches one level (the site's top-level routes, or one prefix route's children):

```text
find(level, path):
    1. If an EXACT route at this level equals path:
           return it                                    # stop everywhere

    2. P := the longest PREFIX or FINAL-PREFIX route at this level
            whose path is a prefix of `path` (may be none)

    3. If P exists and P has child routes:
           r := find(P.children, path)
           if r was chosen by exact, regex, or a final prefix:
               return r                                 # deeper decision wins
           if r is a prefix route:
               P := r                                   # longer, nested prefix

    4. If P is not a FINAL-PREFIX (and no final prefix was chosen deeper):
           for each REGEX route at this level, in configuration order:
               if it matches path:
                   return it                            # first regex wins

    5. return P                                         # may be none
```

If `find(site.routes, path)` finds nothing, the site's **default handler** runs: static file serving from `root`, or `404` if the site has no root.

Consequences (the same as Nginx):

- Exact always wins.
- Regex beats prefix, **unless** the longest prefix is a final prefix (`^~`).
- Regex routes are tried in configuration order, and the first match wins. Specificity doesn't matter.
- Regex routes inside a chosen prefix are tried **before** the regex routes at the outer level.
- How long the prefix is matters only among prefix routes. Configuration order doesn't matter for prefixes.

## 4. Non-path conditions (`match`)

Method, header, query, client IP, scheme and host conditions **never** affect which route is chosen. They go in `match` blocks inside the chosen route:

```text
route "/api/" {
    proxy "api_pool"

    match {
        method [ "POST", "PUT", "DELETE" ]
        client_ip !~ [ "10.0.0.0/8" ]
        return 403
    }
}
```

- Inside a `match` block, every condition must hold (AND). For OR, write separate `match` blocks.
- `match` blocks are checked in configuration order. The **first** one whose conditions all hold applies its actions, which replace the route's actions of the same name. Any later `match` blocks are ignored.
- If no `match` block applies, the route's own actions run.
- Conditions: `method`, `header "<name>" <op> <value>`, `query "<name>" <op> <value>`, `cookie "<name>" <op> <value>`, `client_ip <op> [cidr...]`, `scheme`, `host`. Operators: `=`, `~`, `~*`, `!~`, `!~*`. A bare value means `=`.
- `match` blocks can't be nested.

`lotus config explain <file> <url>` (implemented in `internal/explain`) follows these steps without serving anything. A test starts a real server and checks that explain predicts every status, redirect target and body. So `config explain` always gives two answers: (1) the route, decided by path alone, and (2) the `match` block that applies, decided by the conditions.

## 5. Internal redirects

These restart routing from the top of the **same site** with a new URI:

| Source | Behavior |
|---|---|
| `try_files [ ..., "<uri>" ]` | If none of the earlier entries exist as a file or directory, re-route to the last entry. A last entry of `"=404"` returns that status instead; `"@name"` jumps to a named route. |
| `rewrite '<re>' "<new>" last` | Re-route with the new URI. |
| `error_page 404 "/404.html"` / `"@name"` | Re-route when the handler produces that status. |

Rules:

- At most **10** internal redirects per request. Going over → `500` + an error log entry `LOTUS-RT-0001`.
- Named routes are only reachable by internal redirect. Requests from clients never match `@name`.
- Internal URIs (`try_files` entries, `rewrite` targets) are split at their first `?` **before** placeholders are expanded. The path part expands to decoded text (`{uri}` there means the path alone) and the query part to raw text. Nothing is decoded a second time, so `/a%2520b` stays `/a%2520b`, and a `?` that came from a request value can't start a query.
- `rewrite` runs before `return` and `try_files`; the first rewrite whose pattern matches the path decides, and it matches the normalized, decoded path. A `redirect` or `permanent` rewrite answers 302 or 301 with the expanded target.
- `error_page` replaces a response that LOTUS itself produces with that status (a missing file, a bare `return 404`, `try_files` `=404`). The page is served as a GET (or HEAD) without the request's conditions and ranges, with the original status and the page's own headers. A `return` with explicit text is never replaced. An error page that fails is not replaced again. Error pages count towards the 10 redirects.
- A new URI's query string replaces the old one, unless the new URI has no `?`, in which case the original query is kept. This follows Nginx's `try_files` and `rewrite` behavior.
- Regex capture groups (`{re.1}` …) come from the regex that chose the current route, or from the `rewrite` pattern inside a rewrite.

## 6. Examples

Site routes, in configuration order:

```text
route = "/"                       # A
route "/"                         # B
route "/docs/"                    # C
route ^~ "/static/"               # D
route ~* '\.(png|jpg)$'           # E
route ~ '^/docs/.*\.md$'          # F
route "/docs/api/" {              # G
    route ~ '\.json$'             # G1
}
route "@fallback"                 # H
```

| Request path | Chosen | Why |
|---|---|---|
| `/` | A | Exact |
| `/index.html` | B | Longest prefix `/`; no regex matches |
| `/logo.png` | E | Prefix B, but regex E matches |
| `/static/logo.png` | D | Longest prefix is final → regex skipped |
| `/docs/intro.md` | F | Prefix C, regex F matches (E doesn't) |
| `/docs/guide.html` | C | Prefix C; no regex |
| `/docs/api/spec.json` | G1 | Prefix G → child regex G1 is tried before outer regexes |
| `/docs/api/readme.md` | F | G chosen, G1 doesn't match, outer regex F matches |
| `/docs/api/x.png` | E | G chosen, G1 doesn't match, E matches before F |
| `/@fallback` | B | Named routes are never matched by request paths |

## 7. Mapping to Nginx

| Nginx | LOTUS |
|---|---|
| `location = /p` | `route = "/p"` |
| `location /p` | `route "/p"` |
| `location ^~ /p` | `route ^~ "/p"` |
| `location ~ re` | `route ~ 're'` |
| `location ~* re` | `route ~* 're'` |
| `location @name` | `route "@name"` |
| `if ($request_method = POST) { return 403; }` | `match { method "POST"  return 403 }` (only the safe subset of `if` is translated) |
| `try_files $uri $uri/ /index.php?$args` | `try_files [ "{uri}", "{uri}/", "/index.php?{query}" ]` |

Two differences, documented and covered by tests:

- Nginx `if` inside `location` can change which handler runs in surprising ways (the "if is evil" problem). LOTUS has no `if`. The translator only converts `if` blocks that contain a single `return`, `rewrite` or `set` with a supported condition, and reports everything else as unsupported.
- Nginx regex is PCRE; LOTUS uses RE2. Patterns with backreferences or lookaround can't be translated, and the translator reports them as unsupported.

## 8. Conformance tests

- `internal/router` has table-driven unit tests for every row in §6, plus the edge cases: empty routes, a lone `"/"`, Unicode paths, final prefix inside a nested prefix, overlapping regexes.
- `tests/compatibility/nginx/routing/` contains differential tests. Each fixture holds an Nginx `server` block, the expected LOTUS translation, and a list of request paths. CI runs Nginx in a container, asks each location to return its own name, and checks that LOTUS picks the same route for every path.
- Property test: for randomly generated route sets and paths, the router gives the same answer as a slow reference implementation written directly from §3.
