#!/usr/bin/env bash
# Build TailConnect for Linux on a Linux machine of the target architecture
# (the native window helper needs cgo + GTK/WebKitGTK 4.1 headers):
#   sudo apt-get install -y libgtk-3-dev libwebkit2gtk-4.1-dev
#   scripts/build-linux.sh [version]
# Produces dist/tailconnect_<v>_linux_<arch>.tar.gz, .deb and .rpm.
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)}"
VERSION="${VERSION#v}"
ARCH="$(go env GOARCH)"
OUT="dist"
BUILD="$OUT/build-linux-$ARCH"
rm -rf "$BUILD"
mkdir -p "$BUILD"
LD="-s -w -X main.version=$VERSION"

echo "==> tailconnect ($ARCH)"
CGO_ENABLED=0 go build -trimpath -ldflags "$LD" -o "$BUILD/tailconnect" ./cmd/tailconnect
echo "==> tailconnect-window (GTK/WebKitGTK 4.1)"
CGO_ENABLED=1 go build -trimpath -tags webkit -ldflags "-s -w" -o "$BUILD/tailconnect-window" ./cmd/tailconnect-window
go run ./tools/mkicon "$BUILD/icons"
cp "$BUILD/icons/tailconnect.png" "$BUILD/"
cp packaging/linux/tailconnect.desktop LICENSE "$BUILD/"

echo "==> tar.gz"
TGZ="$OUT/tailconnect_${VERSION}_linux_${ARCH}.tar.gz"
tar -C "$BUILD" -czf "$TGZ" tailconnect tailconnect-window tailconnect.png tailconnect.desktop LICENSE

echo "==> deb / rpm"
export VERSION ARCH BUILD
for fmt in deb rpm; do
  go run github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.40.0 package \
    --config packaging/linux/nfpm.yaml --packager "$fmt" --target "$OUT/"
done
ls -la "$OUT" | grep -E "linux|\.deb|\.rpm" || true
