# LOTUS Governance

## Ownership
LOTUS is developed by **Laika IT** and released under the **Apache License 2.0** (see `LICENSE`).

## Decision making
- **Product owner (Laika IT):** decides scope, priorities and releases.
- **Maintainers:** review and merge changes. Every change needs approval from at least one maintainer who didn't write it. Changes touching security-sensitive packages (`fspath`, `security`, `fastcgi`, `proxy`, `certificates`, `control`, `extensions`, `config`) need two.
- **Spec changes:** architecture-level changes (anything marked ⚑ in the technical specification) need a written spec revision approved by the product owner. Smaller design choices are recorded as Architecture Decision Records in `docs/architecture/adr/`.

## Versioning
| Item | Scheme |
|---|---|
| `lotus` binary | SemVer `MAJOR.MINOR.PATCH` (pre-1.0: minor releases may break things, and the release notes say so) |
| Configuration language | Integer `lotus_config_version`. LOTUS accepts the current version and the one before it. `lotus config migrate` upgrades files. |
| Control protocol | `lotus.control.vN`. Adding methods is allowed. Renaming or removing them needs `vN+1`. |
| Extension API | SemVer, separate from the binary |

Stable directives keep their meaning for the whole major version. Deprecation: warn for at least one minor release, then remove in the next major.

## Support policy
- **Supported releases:** the latest minor release and the one before it.
- **Security fixes:** backported to both supported minor releases.
- **Bug fixes:** only in the latest minor release, unless the bug is severe.
- **Go toolchain:** built with the newest Go release. `go.mod` requires the release before it (Go's own support window).
- **Platforms:** support follows the tier table in the specification (§2.1).

## Dependencies
- New dependencies need a short justification in the PR: why the standard library isn't enough, the license, whether it's maintained, and how many dependencies it pulls in.
- Allowed licenses: Apache-2.0, MIT, BSD-2/3-Clause, ISC, MPL-2.0 (files unmodified). CI blocks anything else.
- `govulncheck` runs on every PR and every night.

## Code of conduct and contributions
Follow `CONTRIBUTING.md` (to be added once outside contributions are opened). The Developer Certificate of Origin (`Signed-off-by`) is required on commits.
