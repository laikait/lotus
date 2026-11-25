# LOTUS Configuration Language — Grammar and Semantics

**Language version:** 1 (`lotus_config_version 1`)
**Status:** Phase 0 draft for review
**Spec reference:** LOTUS Technical Specification v1.1, §5

This document defines the syntax (lexical rules + EBNF) and the meaning of the LOTUS configuration language. The parser, formatter and validator are built from it. Behavior that isn't written down here is undefined, and the parser must reject it.

---

## 1. Design rules

1. **One generic syntax.** The parser knows nothing about specific directives such as `site` or `route`. It produces a generic tree of directives. A separate *schema* (the directive registry) checks names, contexts, argument types and placeholders.
2. **No surprises.** A singular directive appearing twice is an error, never "last one wins". Unknown directives are errors.
3. **Load-time and request-time values look different.** `${name}` is replaced when the config loads; `{name}` is a placeholder resolved per request.
4. **Fully deterministic.** The same input files always produce the same tree, on every platform.

## 2. Encoding and source files

- Files are UTF-8. A leading BOM is ignored. Invalid UTF-8 is an error.
- Line endings may be LF or CRLF. Positions are reported as `file:line:column`: lines start at 1, columns start at 1 and count Unicode code points.
- File extension: `.conf`.
- Maximum file size: 16 MiB. Maximum nesting depth: 64 blocks.

## 3. Lexical grammar

### 3.1 Whitespace, comments and terminators

```ebnf
whitespace   = { " " | "\t" } ;
newline      = "\n" | "\r\n" ;
comment      = "#" , { any_char - newline } ;
terminator   = newline | ";" ;
```

- A comment runs to the end of the line. There are no block comments.
- Newlines end a directive, **except** inside `[ ]` or `( )`, where they count as whitespace.
- `;` lets you put several directives on one line. The formatter rewrites them onto separate lines.

### 3.2 Identifiers

```ebnf
ident        = ident_start , { ident_char } ;
ident_start  = "a" … "z" | "_" ;
ident_char   = ident_start | "0" … "9" ;
variable     = "$" , ident ;
```

Directive names and enum words (`auto`, `http`, `https`) are lowercase identifiers. Identifiers are case-sensitive. `Root` is an error, not an alias of `root`.

Reserved words: `true`, `false`, `let`, `include`, `null`.

### 3.3 Strings

```ebnf
string        = interp_string | raw_string ;
interp_string = '"' , { str_char | escape | interpolation } , '"' ;
raw_string    = "'" , { any_char - "'" } , "'" ;
escape        = "\" , ( '"' | "\" | "n" | "t" | "r" | "$" | "{" | "}" | "u{" , hex , { hex } , "}" ) ;
interpolation = "${" , ident , "}" ;
```

- `\{` and `\}` produce a literal brace that is **never** a placeholder delimiter (§6.3). The AST keeps them marked as escaped.
- `\u{...}` takes 1–6 hex digits and must be a valid Unicode scalar value. Control characters can't be produced this way; use `\n`, `\t` or `\r`.
- Control characters (below U+0020 except tab, and U+007F) can't appear literally in any string.
- A bare `$` not followed by `{` is an ordinary character.

- **Double-quoted** strings allow escapes and `${name}` interpolation. Any other escape (e.g. `"\."`) is an error, so a regex can't silently lose a backslash.
- **Single-quoted** strings are raw: no escapes, no interpolation, and no `'` inside. Use them for regular expressions and Windows paths: `'\.php$'`, `'C:\LOTUS\www'`.
- Neither kind may contain a literal newline. Use `\n` in a double-quoted string.
- `{uri}`, `{host}` etc. inside any string are ordinary text to the parser. The schema decides whether a directive treats them as request-time placeholders (§6.3).
- A literal `${` is written `\${` in a double-quoted string, or put in a single-quoted string.

### 3.4 Numbers, sizes and durations

```ebnf
integer   = digit , { digit | "_" } ;              (* 10_000 *)
size      = integer , size_unit ;
size_unit = "b" | "kb" | "mb" | "gb" | "tb"         (* powers of 10   *)
          | "kib" | "mib" | "gib" | "tib" ;         (* powers of 2    *)
duration  = dur_part , { dur_part } ;               (* 1h30m *)
dur_part  = integer , dur_unit ;
dur_unit  = "ms" | "s" | "m" | "h" | "d" ;
```

- The lexer takes the longest match: `10mb` is a size, `10m` is a duration, `10ms` is a duration.
- A combined duration must list units from largest to smallest, each at most once (`1h30m` is valid, `30m1h` is not).
- `_` may only appear between digits.
- No negative numbers, no fractions. Directives that need a ratio use a percentage string (`"75%"`), which the schema validates.
- Integers must fit in a signed 64-bit integer. Sizes and durations must not overflow it after conversion (bytes, nanoseconds).

### 3.5 Operators and punctuation

```ebnf
operator  = "=" | "~" | "~*" | "^~" | "!~" | "!~*" ;
punct     = "{" | "}" | "[" | "]" | "(" | ")" | "," ;
```

Operators are only meaningful as directive arguments (e.g. route modifiers, §6.4). Their meaning comes from the schema.

## 4. Syntactic grammar

```ebnf
file        = { terminator } , { statement , { terminator } } , EOF ;

statement   = let_stmt | include_stmt | directive ;

let_stmt    = "let" , variable , "=" , value , terminator ;
include_stmt= "include" , string , terminator ;

directive   = ident , { argument } , [ block ] , terminator
            | pair ;
pair        = string , "=" , value , terminator ;      (* map entry; key is always quoted *)

argument    = value | operator ;
block       = "{" , { terminator } , { statement , { terminator } } , "}" ;

value       = scalar | list | call | variable ;
scalar      = string | integer | size | duration | "true" | "false" | "null" | ident ;
list        = "[" , [ value , { "," , value } , [ "," ] ] , "]" ;
call        = ident , "(" , [ value , { "," , value } , [ "," ] ] , ")" ;
```

Notes:

- A `block` ends its directive. After the closing `}` the next token must be a newline, `;`, another `}` (closing the parent block) or the end of the file. Anything else on the same line is an error: `site "x" { } extra` is invalid.
- A directive without a block ends at a newline or `;`. The last directive in a block may also be ended by the closing `}` (`limits { max_request_body 10mib }`), and the last directive in a file by the end of the file.
- The opening `{` must be on the same line as the directive. A `{` at the start of a line is an error.
- In `call`, the `(` must follow the name **immediately**, with no whitespace: `env("X")` is a call; `env ("X")` is not.
- Newlines inside `[ ]` and `( )` are whitespace. If a bracket is never closed, the rest of the file is read as part of it, and the parser reports one "never closed" error at the opening bracket.
- `ident` as a scalar is an **enum word** (`protocol http`, `max_procs auto`). Whether it's valid depends on the schema.
- `pair` is how maps are written: a block whose entries are `"key" = value`. Keys must be quoted, so a pair can't be confused with a directive that takes an `=` operator argument (`route = "/x"`).
- A directive can be written with an `=` as its first argument (an exact route). `let` is the only statement that uses `=` for assignment.

### 4.1 Example parse

```text
route ~ '\.php$' {
    php "php83"
}
```

→ `Directive{name: "route", args: [Op("~"), RawString("\.php$")], block: [Directive{name: "php", args: [String("php83")]}]}`

## 5. Built-in functions

Only these functions exist. They run **once at load time**. Calling an unknown function is an error.

| Function | Result | Notes |
|---|---|---|
| `env("NAME")` | string | Error if the variable isn't set. The value is marked **secret** (redacted in `inspect`, logs and errors). |
| `env("NAME", "default")` | string | Uses the default if the variable isn't set. Secret. |
| `file("path")` | string | Contents of the file (≤ 64 KiB, trailing newline removed). Path is relative to the current file. Secret. |

Functions can't be nested, and their arguments must be literals or `${name}` strings.

## 6. Semantics

### 6.1 Root file and version

- The first statement of the root file must be `lotus_config_version <integer>`.
- Included files must **not** declare a version. They inherit the root's.
- The parser accepts the current version and the one before it (N and N−1). Older versions are rejected with a hint to run `lotus config migrate`.

### 6.2 Variables (`let`)

- `let` is only allowed at the top level of a file (the parser reports `LOTUS-CFG-0016` for a `let` inside a block), before it's used.
- A variable is visible in that file and every file it includes after the `let` statement (lexical, in file order).
- Redefining a variable is an error.
- Usage: `$name` as a whole value, or `${name}` inside a double-quoted string. A variable holding a list can only be used as a whole value.
- Using an undefined variable is an error.
- If any part of a value is secret, the whole value is secret.

### 6.3 Request-time placeholders

- Syntax inside strings: `{name}` or `{name.sub}`, e.g. `{uri}`, `{query}`, `{host}`, `{scheme}`, `{remote_ip}`, `{request_id}`, `{header.User-Agent}`, `{query.page}`, `{cookie.session}`, `{re.1}` (regex capture group).
- Only directives the schema marks as **placeholder-capable** interpret them (e.g. `try_files`, `rewrite`, `return`, `header`, `proxy_header`, `log_format`, `cache_key`).
- In any other directive, a `{name}` that is a known placeholder is a **validation error** (it catches mistakes like `root "/var/www/{host}"`). Literal braces there must be written `\{` in a double-quoted string.
- Unknown placeholder names are errors.
- Placeholder values are never interpreted again (no recursive expansion). Values used in redirects/headers are encoded for their context (CR/LF and control characters are rejected).

### 6.4 Route modifiers (summary)

| Syntax | Meaning |
|---|---|
| `route "/p"` | prefix |
| `route = "/p"` | exact |
| `route ^~ "/p"` | prefix, stop before regex routes when it's the longest prefix |
| `route ~ '<re>'` | regex, case-sensitive (RE2) |
| `route ~* '<re>'` | regex, case-insensitive |
| `route "@name"` | named route (only reachable by internal redirect) |

Full precedence rules: `docs/architecture/routing-precedence.md`.

### 6.5 Includes

- `include "<path or glob>"`: the path is relative to the directory of the file containing it. Absolute paths are allowed.
- Globs use `*`, `?` and `[...]` (Go `path/filepath.Match`) on the last path element only. Matches are sorted by byte order, so results don't depend on the platform.
- A glob that matches nothing is allowed (an empty include directory is normal). A non-glob path that doesn't exist is an error.
- Included statements take the place of the `include` statement, in the same context: an include inside `site { }` adds directives to that site.
- Limits: depth 16, 1,024 files in total. Cycles (the same canonical path included again while it's still being processed) are errors.
- On Unix, included files and their directories must not be world-writable, and must be owned by root or the LOTUS user. Otherwise it's an error (configurable for development with `--insecure-includes`).
- **Tenant boundary:** a file included from inside a `site` may only contain tenant-safe directives. The validator decides this by file: a directive inside a site whose source file differs from the file that defines the site is "from an include", and every directive the registry marks `TenantForbidden` is rejected there (`LOTUS-CFG-0131`). The forbidden directives are those that change where the site is served, what it can read or write, or which process runs it: `root`, `aliases`, `listeners`, `tls`, `logs`, `limits`, `htaccess`, `htaccess_allow`, `htaccess_unsupported`, `htaccess_recheck` (whether and how `.htaccess` files are read), `php` (backend choice), and everything that only exists at server level (`server`, `listen`, `runtime`, `trusted_proxies`, `acme`, `php_backend`, `control`). Allowed: `index`, `route` (and what is inside it, except `php` and `root`), `try_files`, `return`, `match`, `php_deny`, `security_headers`, `max_request_body`. A site that is itself defined in an included file may set all of its own directives, because the check compares against that file.

- **Secrets in messages:** values produced by `env()` and `file()` (and anything built from them) are marked secret. Diagnostics never print them: an error about a secret value says the value is hidden, and secret names, paths and keys are replaced by `(secret value)` / `(secret path)`.

### 6.6 Duplicates and merging

The schema gives each directive a **cardinality**:

| Cardinality | Rule |
|---|---|
| `single` | At most once per context. A second occurrence (including one from an include) is an error showing both locations. |
| `repeated` | Any number. Order is kept (e.g. `route`, `header`). |
| `keyed` | Repeated, but the first argument must be unique per context (e.g. `site "<name>"`, `listen "<name>"`, `php_backend "<name>"`). |

Inheritance works by **context**, not by text: a value set in `server` (e.g. `limits`) is the default for every `site` unless the site sets it. Inherited blocks are merged one field at a time. The schema says which directives can be inherited.

### 6.7 Types

The schema checks each argument against one of these types:

`string`, `int`, `bool`, `size`, `duration`, `enum(a|b|c)`, `cidr`, `address` (`host:port`, `unix:/path`, `pipe:name`), `path`, `regex` (RE2, compiled during validation), `percent`, `list<T>`, `map<string,T>`, `placeholder_string`.

Conversions are strict: a `string` argument doesn't accept `10mb`, and an `int` doesn't accept `"10"`.

`null` is only valid where the schema allows it. It means "turn off an inherited value", e.g. `access_log null`.

### 6.8 Paths

- Relative paths in directives are relative to the **directory of the root file**, not the current working directory.
- On Windows, forward slashes are accepted and normalized. Single-quoted strings avoid having to escape backslashes.

## 7. Errors

Every error contains: severity, code (`LOTUS-CFG-0001` …), `file:line:column`, a message, and an optional hint (e.g. "did you mean `max_request_body`?", edit distance ≤ 2). The parser keeps going after an error and reports up to 50 errors per run. `--json` prints them as a JSON array.

Error codes are stable across releases and listed in `docs/configuration/errors.md` (generated).

## 8. Formatter rules (`lotus config format`)

Implemented in `internal/config/format`. `lotus config format <file>` prints the formatted file, `--write` rewrites it, `--check` lists files that would change (exit 9). A file with syntax errors is never formatted. Includes are not followed: each file is formatted on its own.

- 4-space indentation. One directive per line. One space between arguments.
- Opening `{` on the same line as the directive. `}` on its own line.
- Lists: on one line if the whole line is ≤ 100 columns. Otherwise one item per line with a trailing comma.
- Comments are kept. An own-line comment stays attached to the directive below it; a comment on the same line after a directive stays on that line; comments before a closing `}` or the end of the file stay at the end of their block or file. A comment right after an opening `{` moves to its own line above the block's first directive.
- A comment inside a multi-line `[ ]` or `( )` moves to just above the next directive (its original position isn't tracked).
- At most one blank line in a row is kept. No blank lines right after `{` or before `}`.
- String quoting is kept as written. Units and number spellings are kept as written.
- Formatting must not change meaning, and running it twice gives the same result. CI checks `parse(format(x)) == parse(x)` on the fuzz corpus.

## 9. Conformance

- `tests/fixtures/config/valid/*.conf` must parse, each with a golden AST file.
- `tests/fixtures/config/invalid/*.conf` must fail with the expected error code and position (written in a header comment, e.g. `# expect: LOTUS-CFG-0012 3:9`).
- The parser is fuzzed (`go test -fuzz`) from Phase 1 onward. It must never panic or hang, and memory use must stay below 64× the input size.

## 10. Complete example

```text
lotus_config_version 1

let $webroot = "/var/www"

server {
    runtime {
        max_procs auto
        memory_limit 2gib
    }

    listen "http" {
        address "0.0.0.0:80"
        protocol http
    }

    listen "https" {
        address "0.0.0.0:443"
        protocol https
        http3 true
    }

    trusted_proxies [ "10.0.0.0/8" ]

    tls {
        min_version "1.2"
        acme {
            email env("LOTUS_ACME_EMAIL")
        }
    }

    php_backend "php83" {
        fastcgi_pass "unix:/run/php/php8.3-fpm-example.sock"
        timeout 60s
    }

    site "example.com" {
        listeners [ "http", "https" ]
        aliases [ "www.example.com" ]
        tls automatic
        root "${webroot}/example/public"
        index [ "index.php", "index.html" ]

        security_headers {
            "X-Frame-Options" = "SAMEORIGIN"
            "Referrer-Policy" = "strict-origin-when-cross-origin"
        }

        route "/" {
            try_files [ "{uri}", "{uri}/", "/index.php?{query}" ]
        }

        route ~ '\.php$' {
            php "php83"
        }

        route ^~ "/uploads/" {
            php_deny true
        }

        include "sites.d/example/*.conf"
    }
}
```
