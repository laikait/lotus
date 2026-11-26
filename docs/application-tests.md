# Application tests

`internal/apptest` runs real PHP applications behind the real LOTUS binary. Each site is made with `sitefile` (the web admin's own code), so the **site templates** of the admin are what is tested: the file the Sites page writes for a WordPress or Laravel site must run that application.

They are off by default (they download the applications, need PHP and a database, and take a minute or two). Turn them on with environment variables:

| Variable | Meaning |
|---|---|
| `LOTUS_APPTEST_CACHE` | a folder for downloads (the WordPress zip, the Laravel project, Composer's cache); setting it turns the tests on |
| `LOTUS_PHP_CGI`, `LOTUS_PHP` | the `php-cgi` and `php` programs (default: found in PATH). PHP 8.2 or newer for Laravel 12 |
| `LOTUS_APPTEST_DB` | `host:port` of a MySQL or MariaDB server that accepts user `root` without a password. The WordPress test makes its own database and drops it afterwards. Use a throw-away server, not one that holds data (`mysqld --skip-grant-tables --datadir=<new folder> --port=3399` works) |
| `LOTUS_COMPOSER` | the Composer command (Laravel only) |

```text
go test ./internal/apptest -run . -v -timeout 30m
```

## What is checked

**WordPress** (latest release; the 7.x line at the time of writing): the installer page and the install step; the front page, a post by id, and the REST API with plain permalinks; signing in and the dashboard; saving pretty permalinks (`/%postname%/`) and then serving `/hello-world/`, `/sample-page/`, the feed and `/wp-json/` through the template's front controller; a missing page is a real 404; a static script is served with a JavaScript content type; an upload through the media screen and the uploaded file's URL; a `.php` file placed in `wp-content/uploads` is never run (403); a text file there is served; `.htaccess` is not served.

**Laravel** (`laravel/laravel` from Composer; 12.x at the time of writing): the welcome page, `/up`, `/index.php/up` (PATH_INFO), Laravel's own 404 for an unknown URL, nothing outside `public/` reachable (`.env`, `artisan`, `composer.json`, `vendor/`, the log, `.git`), a static file of `public/`, and a POST that Laravel answers itself (419 or 405).

## Results (2026-11-26)

Both pass on Windows with XAMPP's PHP 8.2.12 (ZTS) and MariaDB 10.4. They were not run on Linux, with PHP-FPM, or in CI. Symfony, Drupal and Joomla have no test yet.
