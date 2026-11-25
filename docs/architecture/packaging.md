# Packaging, services and releases

**Status:** `lotus service`, the Windows service mode, `scripts/release.sh` and the release workflow are implemented. Signed binaries, `.deb`/`.rpm` packages and a Windows installer are not built yet.

## Running as a service

```text
lotus service unit      [--config FILE] [--user NAME] [--binary PATH]   print the definition
lotus service install   [--config FILE] [--user NAME] [--binary PATH] [--force]
lotus service uninstall
```

**Linux (systemd).** `sudo lotus service install` writes `/etc/systemd/system/lotus.service` and prints what to run next (create the `lotus` account, `systemctl enable --now lotus`). The unit runs the server as that account, not root; `CAP_NET_BIND_SERVICE` lets it bind ports 80 and 443. `ExecReload` is `lotus reload` and `ExecStop` is `lotus stop` (graceful, 30 s for requests to finish). The control socket is `/run/lotus/control.sock` (`RuntimeDirectory=lotus`), certificates and state go to `/var/lib/lotus`, logs to `/var/log/lotus`. Hardening: `NoNewPrivileges`, `ProtectSystem=strict` (the file system is read-only except those directories: add your document roots' upload and log directories with `ReadWritePaths=`), `ProtectHome=read-only`, `PrivateTmp`, `PrivateDevices`, kernel and control-group protection, address families limited to IP and Unix sockets.

**FreeBSD.** `lotus service unit` prints an `rc.d` script; save it as `/usr/local/etc/rc.d/lotus`, make it executable and set `lotus_enable="YES"` in `/etc/rc.conf`.

**Windows.** In an administrator shell, `lotus service install --config C:\ProgramData\LOTUS\main.lotus` registers the `LOTUS` service: starts at boot, restarts after a crash (after 2 s, 10 s, then a minute). Start it with `net start LOTUS`. Under the service manager the server's output goes to `C:\ProgramData\LOTUS\logs\lotus.log` (a service has no console), a stop request from the manager is a graceful stop, and `lotus reload` / `lotus status` work as on any platform. PHP through `php_backend { command ... }` needs no PHP-FPM (`docs/architecture/php.md`).

`install` never starts the service and never creates accounts or directories outside the one file or registration it describes.

## Release archives

`make dist` (or `scripts/release.sh VERSION`) builds, with `CGO_ENABLED=0 -trimpath`, one archive per platform (`linux/amd64`, `linux/arm64`, `freebsd/amd64`, `windows/amd64`, `darwin/arm64`) holding the program, `LICENSE`, `README.md`, `SECURITY.md` and the examples, plus `SHA256SUMS`. Pushing a tag `vX.Y.Z` runs `.github/workflows/release.yml`, which tests, builds and creates a **draft** GitHub release with those files; a person reads it and publishes it.

Verify a download:

```text
sha256sum -c SHA256SUMS --ignore-missing          (Linux)
Get-FileHash lotus-X.Y.Z-windows-amd64.zip        (Windows; compare with SHA256SUMS)
```

## "Unknown publisher" on Windows

The program is not signed yet, so Windows shows "Unknown publisher" (when it asks for administrator rights) and SmartScreen may warn about a download. Neither means the file is bad; it means nobody vouches for it. Until releases are signed:

- check the SHA-256 against `SHA256SUMS` from the release page;
- after checking, remove the download mark with `Unblock-File lotus.exe`.

The plan for signed releases: Authenticode-sign `lotus.exe` with a timestamp in the release workflow, using a certificate whose key lives in a hardware module or a signing service (SignPath Foundation offers signing to open-source projects free of charge; Azure's managed signing service and an OV/EV certificate are the alternatives). A signature gives the publisher name; SmartScreen reputation still needs downloads over time with a stable signer. This needs a decision on the publisher identity, so it is not wired in yet.

## Not built yet

`.deb` and `.rpm` packages (with the `lotus` account and directories created by the package), a Windows installer (MSI/MSIX), a Homebrew formula, build provenance attestations, and the signing step above.
