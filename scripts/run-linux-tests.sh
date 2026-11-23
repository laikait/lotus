#!/usr/bin/env bash
# Runs the Linux test binaries built by scripts/test-linux-wsl.sh. This script
# runs inside WSL (or any Linux machine). Usage: run-linux-tests.sh <repo-root>
set -u
root="$1"
out="$root/bin/linux-tests"
work=$(mktemp -d)
fail=0
for t in "$out"/*.test; do
  name=$(basename "$t" .test)
  cp "$t" "$work/$name.test"
  chmod +x "$work/$name.test"
  dir="$root$(cat "$out/$name.dir")"
  echo "== $name"
  # Tests run in their package directory, as under "go test".
  (cd "$dir" && "$work/$name.test" -test.count=1 -test.v 2>&1) > "$work/$name.log"
  status=$?
  grep -E '^(--- (FAIL|SKIP)|ok|PASS|FAIL|panic)|^\s+--- (FAIL|SKIP)|_test.go:[0-9]+:' "$work/$name.log" | head -40
  [ "$status" -eq 0 ] || fail=1
done
rm -rf "$work"
exit $fail
