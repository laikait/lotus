#!/usr/bin/env bash
# Builds the release archives into dist/ : one per platform, with the program, the
# licence, the README and the examples, and SHA256SUMS over all of them.
#
#   scripts/release.sh [version]        (default: git describe)
#
# The archives are reproducible enough to compare: -trimpath, no cgo, a fixed
# timestamp from the commit. Signing is a separate step (docs/packaging.md).
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)}"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
EPOCH="$(git log -1 --format=%ct 2>/dev/null || date +%s)"
DATE="$(date -u -d "@$EPOCH" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)"
MODULE=github.com/laikait/lotus
LDFLAGS="-s -w -X $MODULE/pkg/version.Version=$VERSION -X $MODULE/pkg/version.Commit=$COMMIT -X $MODULE/pkg/version.Date=$DATE"
PLATFORMS="${PLATFORMS:-linux/amd64 linux/arm64 freebsd/amd64 windows/amd64 darwin/arm64}"

rm -rf dist
mkdir -p dist
for p in $PLATFORMS; do
  os="${p%/*}"; arch="${p#*/}"
  name="lotus-${VERSION}-${os}-${arch}"
  stage="dist/$name"
  ext=""; [ "$os" = windows ] && ext=".exe"
  echo "building $name"
  mkdir -p "$stage"
  GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$stage/lotus$ext" ./cmd/lotus
  cp LICENSE README.md SECURITY.md "$stage/"
  cp -r configs/examples "$stage/examples"
  if [ "$os" = windows ]; then
    (cd dist && python3 -c "
import zipfile,os,sys
n='$name'
with zipfile.ZipFile(n+'.zip','w',zipfile.ZIP_DEFLATED) as z:
    for r,_,fs in os.walk(n):
        for f in sorted(fs):
            p=os.path.join(r,f); z.write(p,p)
" 2>/dev/null || zip -qr "$name.zip" "$name")
  else
    tar --sort=name --owner=0 --group=0 --numeric-owner --mtime="@$EPOCH" -C dist -czf "dist/$name.tar.gz" "$name" 2>/dev/null \
      || tar -C dist -czf "dist/$name.tar.gz" "$name"
  fi
  rm -rf "$stage"
done
(cd dist && (sha256sum lotus-* 2>/dev/null || shasum -a 256 lotus-*) > SHA256SUMS)
echo; cat dist/SHA256SUMS
