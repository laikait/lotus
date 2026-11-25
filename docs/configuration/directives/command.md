# `command`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

## In `dns`

```text
command <path>
```

| | |
|---|---|
| **Arguments** | `path`: path |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Program for the exec provider. Started without a shell.

### Security

It runs as the LOTUS user; make it writable by administrators only.

## In `php_backend`

```text
command <path>
```

| | |
|---|---|
| **Arguments** | `path`: path |
| **How often** | at most once |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | anyone who can edit the file |
| **Since** | LOTUS 0.1, config language 1 |

### Description

A php-cgi program that LOTUS starts and supervises instead of connecting to an existing server. Use this where there is no PHP-FPM, such as Windows.

### Security

The processes run as the LOTUS user and get a minimal environment (no LOTUS secrets). On Windows they listen on 127.0.0.1, so any account on the machine can reach them.
