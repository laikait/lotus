# `site`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
site <name> { ... }
```

| | |
|---|---|
| **Contexts** | `server` |
| **Arguments** | `name`: hostname |
| **Block** | required, with the directives of the `site` context |
| **How often** | any number of times; the first argument is a unique name |
| **Default** | none |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

A virtual host. The name is its primary hostname; aliases add more.

### Security

Sites are separated by hostname only. LOTUS reads every site's static files as its own user, so isolate tenants with file permissions and the symlink policy.

### Example

```text
site "example.com" {
    root "/var/www/example"
}
```
