# `htaccess_allow`

<!-- Generated from the directive registry by `make docs`. Do not edit by hand. -->

```text
htaccess_allow <categories>
```

| | |
|---|---|
| **Contexts** | `site` |
| **Arguments** | `categories`: list of one of rewrite|redirect|error_document|directory_index|options|access|headers|php |
| **How often** | at most once |
| **Default** | all supported categories |
| **Request-time placeholders** | no |
| **Who can set it** | administrators only: refused in files a site includes for its tenant |
| **Since** | LOTUS 0.1, config language 1 |

### Description

Which kinds of directives .htaccess files may use: rewrite, redirect, error_document, directory_index, options, access, headers, php. A directive of another kind is treated as unsupported.

### Security

Leave out access to stop .htaccess from relaxing or adding access rules; leave out headers to stop it adding response headers.

### Example

```text
htaccess true
htaccess_allow [ "rewrite", "redirect" ]
```
