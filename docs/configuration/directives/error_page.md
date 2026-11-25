# `error_page`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
error_page <status> <target>
```

| | |
|---|---|
| **Contexts** | `route`, `site` |
| **Arguments** | `status`: integer<br>`target`: string |
| **How often** | any number of times, in order |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Shows another page for responses with this status that LOTUS produces itself (a missing file, return or try_files =404). The target is a path such as "/404.html" or a named route such as "@error". The visitor still gets the original status code. Routes inherit the site's error pages and can replace them one by one.

### Security

The error page is read from the same document root, under the same rules as any file. An error page that itself fails is not replaced again, so there are no loops.
