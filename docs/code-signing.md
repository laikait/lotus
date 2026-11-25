# Code signing policy

LOTUS release binaries for Windows are signed so that Windows shows a publisher name instead of "Unknown publisher". This page says what is signed, by whom and how.

*Free code signing for Windows is provided by [SignPath.io](https://signpath.io), with a certificate from the [SignPath Foundation](https://signpath.org).* (Applies once the project is accepted; until then releases are unsigned and carry a SHA-256 checksum, see `docs/architecture/packaging.md`.)

## What is signed

Only `lotus.exe` in the Windows release archive, and only when it was built from this repository (`github.com/laikait/lotus`) by the release workflow (`.github/workflows/release.yml`) on GitHub Actions, from a version tag. Nothing built on a developer's machine is signed. The program contains LOTUS and its Go dependencies listed in `go.mod`; there are no binary blobs.

## Roles

| Role | Who | What they do |
|---|---|---|
| Authors and committers | the people with write access to the repository | change the code; every change to `main` goes through review where there is more than one maintainer |
| Reviewers | the maintainers | read a change before it is merged |
| Approvers | the maintainers named in `GOVERNANCE.md` | approve each signing request (a version tag) in SignPath |

A tag is created only by an approver. A signing request is approved only after the draft release has been checked: the CI results are green, the archive's `SHA256SUMS` match, and the version is the one intended.

## Privacy

LOTUS does not collect or send data about its users. The program makes network connections only as the administrator configures it (listeners, backends, and ACME certificate authorities when `tls automatic` is used).

## Reporting a problem with a signed release

Follow `SECURITY.md`. A signature that should not have been made is reported to SignPath as well, so the certificate can be handled.

## For maintainers: switching signing on

1. Get the project accepted by SignPath Foundation, and note the organization ID, project slug and signing-policy slug.
2. In the GitHub repository settings add the variable `SIGNPATH_ORGANIZATION_ID`, and optionally `SIGNPATH_PROJECT_SLUG` (default `lotus`) and `SIGNPATH_POLICY_SLUG` (default `release-signing`), and the secret `SIGNPATH_API_TOKEN`.
3. In SignPath create an artifact configuration for a single file, `lotus.exe`, signed with Authenticode (SHA-256, with a timestamp).
4. Push a version tag. The workflow signs `lotus.exe`, puts it into the Windows archive and rewrites `SHA256SUMS`; the draft release then holds the signed file.
