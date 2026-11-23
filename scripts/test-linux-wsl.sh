#!/usr/bin/env bash
# Runs the whole test suite as Linux binaries inside WSL, from a Windows machine.
#
# Some tests only run on Unix (symlinks, file ownership and permissions, FIFOs),
# and others skip on Windows because creating symlinks needs a privilege. This
# script cross-compiles each package's tests for Linux and runs them in WSL so
# those tests really execute.
#
# Usage (Git Bash):  scripts/test-linux-wsl.sh [wsl-distro]     (default: Ubuntu)
set -euo pipefail

distro="${1:-Ubuntu}"
root="$(cd "$(dirname "$0")/.." && pwd)"
out="$root/bin/linux-tests"
rm -rf "$out" && mkdir -p "$out"
cd "$root"

pkgs=$(go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./...)
for pkg in $pkgs; do
  name=$(echo "${pkg#*/lotus/}" | tr '/' '_')
  dir="${pkg#github.com/laikait/lotus}"        # "" for the module root, else "/internal/x"
  echo "$dir" > "$out/$name.dir"
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -c -o "$out/$name.test" "$pkg"
done

# Git Bash path /c/dir -> WSL path /mnt/c/dir
drive="${root:1:1}"
wslroot="/mnt/${drive,,}${root:2}"
# The runner is a file (not an inline script) because wsl.exe mangles quotes in
# command-line arguments. The repository path must not contain spaces.
MSYS_NO_PATHCONV=1 wsl.exe -d "$distro" -- bash "$wslroot/scripts/run-linux-tests.sh" "$wslroot"
