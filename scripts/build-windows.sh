#!/usr/bin/env bash
# Build TailConnect for Windows (amd64): TailConnectApp.exe (GUI),
# tailconnect.exe (CLI), a portable zip and, when Inno Setup is available
# (Windows), the installer.
#
#   scripts/build-windows.sh [version]
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)}"
VERSION="${VERSION#v}"
OUT="dist"
BUILD="$OUT/build-windows"
rm -rf "$BUILD"
mkdir -p "$BUILD"

echo "==> Icons and resources"
go run ./tools/mkicon "$BUILD/icons"
cp "$BUILD/icons/tailconnect.ico" "$BUILD/"
NUMVER="${VERSION%%-*}.0"
go run github.com/tc-hib/go-winres@v0.3.3 simply \
  --arch amd64 --out cmd/tailconnect/rsrc \
  --icon "$BUILD/icons/tailconnect.png" --manifest gui \
  --product-name TailConnect --file-description TailConnect \
  --product-version "$NUMVER" --file-version "$NUMVER" \
  --copyright "TailConnect contributors. Apache License 2.0."
trap 'rm -f cmd/tailconnect/rsrc_windows_*.syso' EXIT

echo "==> Building $VERSION"
export GOOS=windows GOARCH=amd64 CGO_ENABLED=0
LD="-s -w -X main.version=$VERSION"
go build -trimpath -ldflags "$LD -H=windowsgui" -o "$BUILD/TailConnectApp.exe" ./cmd/tailconnect
go build -trimpath -ldflags "$LD" -o "$BUILD/tailconnect.exe" ./cmd/tailconnect

echo "==> Portable zip"
ZIP="$OUT/TailConnect-$VERSION-windows-amd64.zip"
rm -f "$ZIP"
(cd "$BUILD" && cp ../../LICENSE LICENSE.txt && \
  if command -v zip >/dev/null; then zip -q "../../$ZIP" TailConnectApp.exe tailconnect.exe LICENSE.txt; \
  else powershell -NoProfile -Command "Compress-Archive -Path TailConnectApp.exe,tailconnect.exe,LICENSE.txt -DestinationPath ../../$ZIP"; fi)

ISCC=""
for c in iscc "/c/Program Files (x86)/Inno Setup 6/ISCC.exe" "C:/Program Files (x86)/Inno Setup 6/ISCC.exe"; do
  if command -v "$c" >/dev/null 2>&1 || [[ -x "$c" ]]; then ISCC="$c"; break; fi
done
if [[ -n "$ISCC" ]]; then
  echo "==> Installer"
  WBUILD=$(cd "$BUILD" && pwd -W 2>/dev/null || pwd)
  WOUT=$(cd "$OUT" && pwd -W 2>/dev/null || pwd)
  # Stop Git Bash from rewriting the /Q and /D options as paths.
  (cd packaging/windows && MSYS2_ARG_CONV_EXCL='*' "$ISCC" /Q "/DAppVersion=$VERSION" "/DBuildDir=$WBUILD" "/DOutDir=$WOUT" tailconnect.iss)
else
  echo "==> Inno Setup not found; skipping installer (build it on Windows)"
fi
echo
ls -la "$OUT" | grep -i windows || true
