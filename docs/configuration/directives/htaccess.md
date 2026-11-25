# `htaccess`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
htaccess <enabled>
```

| | |
|---|---|
| **Contexts** | `site` |
| **Arguments** | `enabled`: bool |
| **How often** | at most once |
| **Default** | false |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Reads .htaccess files in the document root and the directories below it, for the directives in htaccess_allow. Off by default.

### Security

A .htaccess file is written by whoever can write to the site. It can rewrite, redirect and deny URLs of its own directory tree but is never executed as code, and it can't loosen the site's symlink, dotfile or security header settings.

### Example

```text
htaccess true
```
