# How to make the Windows installer

This builds `dist\lotus-VERSION-windows-amd64-setup.exe`, a normal Windows installer for LOTUS. Everything it needs is in the repository: the script `scripts\build-installer.ps1` and the installer definition `installer\lotus.iss`.

## What you need (once)

| Tool | Why | Get it |
|---|---|---|
| Go (the version in `go.mod`) | builds `lotus.exe` | https://go.dev/dl/ |
| Inno Setup 6 or 7 | builds the installer from `installer\lotus.iss` | https://jrsoftware.org/isdl.php (free) |
| Git (optional) | the version number and commit are taken from it | https://git-scm.com/ |

Check that Go works with `go version`. The build script looks for Inno Setup in `C:\Program Files\Inno Setup 7` and `6` (and the `(x86)` and per-user folders) and then on `PATH`; no setting is needed.

## Build it

Open PowerShell in the repository folder (the one that contains `go.mod`) and run:

```powershell
powershell -ExecutionPolicy Bypass -File scripts\build-installer.ps1 -Version 0.1.0
```

`-ExecutionPolicy Bypass` is needed because Windows blocks script files by default; it applies to this one command only and changes no setting.

The script:

1. builds `bin\lotus.exe` for Windows (amd64) with the version, commit and date compiled in;
2. finds Inno Setup and compiles `installer\lotus.iss`;
3. writes `dist\lotus-0.1.0-windows-amd64-setup.exe` and lists it.

Leave out `-Version` to use `git describe` (for example `0.1.0-3-gabc1234`).

## What the installer does

It asks for administrator rights, then:

| What | Where |
|---|---|
| The program, `LICENSE`, `README.md`, the examples | `C:\Program Files\Lotus` |
| The configuration (made only if it does not exist) | `C:\Program Files\Lotus\main.conf` |
| The web directory with a sample page (made only if it does not exist) | `C:\www` |
| The log | `C:\Program Files\Lotus\logs\lotus.log` |
| Optional: `C:\Program Files\Lotus` added to the system `PATH` | ticked by default |
| The web admin account (a page of the wizard; leave the password empty to skip) | `C:\Program Files\Lotus\admin-users.txt`, readable by SYSTEM and Administrators only |

Then it checks the configuration, registers the `LOTUS` Windows service (starts at boot, restarts on failure), starts it, and offers to open the web admin at http://127.0.0.1:7080/ (sign in with the account from the wizard; see [the admin](architecture/admin.md)). The sample site is at http://127.0.0.1:8080/. The Start Menu gets entries to open the admin, edit the configuration, open the log and the site folder, and start, stop or restart the service.

On an upgrade, `main.conf` is not replaced, so an older one has no `admin { address "127.0.0.1:7080" }` block: add it by hand, then `lotus admin passwd NAME`, and restart the service (a change to the admin block is not applied by a reload).

**Upgrades:** run a newer installer over an older one. It stops and removes the old service first and registers it again. The configuration and the web directory are never replaced.

**Uninstall:** Settings → Apps, or the Start Menu entry. It stops and removes the service, the program files and the `PATH` entry. The configuration, the logs and `C:\www` are kept on purpose; delete them by hand if you want them gone.

## Day to day, after installing

- **Change the configuration:** Start Menu → LOTUS → *Edit LOTUS configuration* (`C:\Program Files\Lotus\main.conf`).
- **Check it:** `lotus config validate C:\Program Files\Lotus\main.conf`
- **Add a site:** `lotus site add example.com --root C:/www/example --aliases www.example.com` (makes `C:\Program Files\Lotus\sites.d\example.com.conf`); `lotus site list`, `lotus site remove example.com`. Then reload.
- **Apply it without a restart:** `lotus reload` (from an Administrator shell, because the service runs as SYSTEM)
- **Start, stop, restart:** the Start Menu entries, or `Start-Service LOTUS`, `Stop-Service LOTUS`, `Restart-Service LOTUS` in an Administrator PowerShell, or `services.msc`.

## Change the installer

| To change | Edit |
|---|---|
| Install folder, shortcuts, service steps, `PATH` | `installer\lotus.iss` |
| The default configuration that new installs get | `installer\main.conf` |
| The sample page | `installer\index.html` |
| The logo | replace `installer\lotus.ico`; to put it into `lotus.exe` too, regenerate `cmd\lotus\rsrc_windows_amd64.syso` (see below) |
| The version number | `-Version` on the command line |

The placeholder logo (a lotus flower) is drawn by `tools\mkicon`:

```powershell
go run ./tools/mkicon -syso cmd/lotus/rsrc_windows_amd64.syso
```

This writes `installer\lotus.ico`, `installer\lotus.png` and the `.syso` file that the Go linker puts into `lotus.exe`. To use a designed logo, make an `.ico` with 16, 32, 48 and 256 pixel sizes, put it at `installer\lotus.ico` and ask for the `.syso` to be regenerated from it.

## Troubleshooting

| Problem | Cause and fix |
|---|---|
| "running scripts is disabled on this system" | Use the command above with `-ExecutionPolicy Bypass`. |
| "Inno Setup (6 or 7) was not found" | Install it, or put the folder with `ISCC.exe` on `PATH`. |
| A compile error in `lotus.iss` | The message names the line. The script was written for Inno Setup 6 and has run on 7; a directive that changed is the likely cause. |
| "Unknown publisher" when the installer starts | The installer and `lotus.exe` are not signed yet. See `docs/code-signing.md`. Nothing is wrong with the file. |
| The service does not start | Read `C:\Program Files\Lotus\logs\lotus.log`. Usual causes: the port in `main.conf` is taken (for example by Apache from XAMPP on 80 or 443), or the `root` folder does not exist. |
| The site answers 403 | The folder in `root` has no `index.html`; directory listings are not supported. |
| `lotus reload` says no running server answered | Run it from an Administrator shell. The control socket belongs to the service account. |

## Build a release for all platforms instead

`scripts/release.sh` builds the archives for Linux, FreeBSD, macOS and Windows plus `SHA256SUMS` (see `docs/architecture/packaging.md`). The installer above is separate: it is the friendly way to put LOTUS on a Windows machine, and it is built on Windows with Inno Setup.
