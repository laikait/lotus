# Builds the Windows installer: dist\lotus-VERSION-windows-amd64-setup.exe
#
#   powershell -ExecutionPolicy Bypass -File scripts\build-installer.ps1 [-Version 0.1.0]
#
# (-ExecutionPolicy Bypass is needed where PowerShell blocks scripts; it applies
# to this one command only.)
#
# Needs Go and Inno Setup 6 or 7 (https://jrsoftware.org/isdl.php, free).
param([string]$Version = "")

$ErrorActionPreference = "Stop"
Set-Location (Split-Path $PSScriptRoot -Parent)

if (-not $Version) {
    $Version = (git describe --tags --always --dirty 2>$null)
    if (-not $Version) { $Version = "0.0.0" }
    $Version = $Version.TrimStart("v")
}
# Inno wants a plain version number in the file name; keep anything git adds.
$commit = (git rev-parse --short HEAD 2>$null)
$date = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
$module = "github.com/laikait/lotus"
$ldflags = "-s -w -X $module/pkg/version.Version=$Version -X $module/pkg/version.Commit=$commit -X $module/pkg/version.Date=$date"

$env:GOOS = "windows"; $env:GOARCH = "amd64"; $env:CGO_ENABLED = "0"
New-Item -ItemType Directory -Force bin, dist | Out-Null
go build -trimpath -ldflags $ldflags -o bin\lotus.exe .\cmd\lotus
if ($LASTEXITCODE -ne 0) { throw "go build failed" }

$candidates = @()
foreach ($v in "7", "6") {
    $candidates += "$env:ProgramFiles\Inno Setup $v\ISCC.exe"
    $candidates += "${env:ProgramFiles(x86)}\Inno Setup $v\ISCC.exe"
    $candidates += "$env:LOCALAPPDATA\Programs\Inno Setup $v\ISCC.exe"
}
$iscc = $candidates | Where-Object { Test-Path $_ } | Select-Object -First 1
if (-not $iscc) {
    $cmd = Get-Command ISCC.exe -ErrorAction SilentlyContinue
    if ($cmd) { $iscc = $cmd.Source }
}
if (-not $iscc) { throw "Inno Setup (6 or 7) was not found. Install it from https://jrsoftware.org/isdl.php, or put its folder on PATH, and run this again." }
Write-Host "Using $iscc"

& $iscc "/DAppVersion=$Version" installer\lotus.iss
if ($LASTEXITCODE -ne 0) { throw "Inno Setup failed" }
Get-ChildItem dist\*-setup.exe | Select-Object Name, Length
