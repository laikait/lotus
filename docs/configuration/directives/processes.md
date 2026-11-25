# `processes`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
processes <count>
```

| | |
|---|---|
| **Contexts** | `php_backend` |
| **Arguments** | `count`: integer |
| **How often** | at most once |
| **Default** | 4 |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

How many php-cgi processes to run with command. Each handles one request at a time.

### Security

More processes allow more simultaneous requests and use more memory.

### Example

```text
command "/usr/bin/php-cgi"
processes 8
```
