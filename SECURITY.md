# Security Policy

## Reporting a vulnerability

**Do not open a public issue for security problems.**

Report vulnerabilities privately to the LOTUS security team at: `security@<laika-it-domain>` *(placeholder — set the real address before the first public release)*.

Please include:
- The affected LOTUS version(s) and platform.
- A description of the issue and its impact.
- Steps to reproduce, or a proof of concept.
- Whether you want to be credited, and under what name.

## Our commitments

| Step | Target |
|---|---|
| Acknowledge the report | within 2 business days |
| Initial assessment and severity (CVSS v4) | within 7 days |
| Fix for Critical/High issues | within 30 days |
| Fix for Medium/Low issues | next scheduled release |
| Public disclosure | coordinated with the reporter; normally ≤ 90 days after the report |

We publish a security advisory and request a CVE for every confirmed vulnerability in a released version.

## Supported versions

Security fixes go to the two most recent minor releases (see `GOVERNANCE.md`).

## Scope

In scope: the `lotus` binary, official packages, the extension SDK and its sandbox, default configurations and templates.

Out of scope: vulnerabilities in PHP applications served by LOTUS, in PHP-FPM, or in third-party extensions. However, ways for one site to escape another site's isolation through LOTUS **are** in scope.
