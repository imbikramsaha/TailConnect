#!/usr/bin/env bash
# Build TailConnect.app (universal: Apple Silicon + Intel) and a .dmg.
#
#   scripts/build-macos.sh [version]
#
# Signing (optional):
#   SIGN_IDENTITY="Developer ID Application: Name (TEAMID)"  sign for distribution
#   NOTARY_PROFILE=<notarytool keychain profile>             also notarize + staple
# Without SIGN_IDENTITY the app is ad-hoc signed: it runs on this Mac, but
# other Macs will show a Gatekeeper warning (right-click → Open to bypass).
set -euo pipefail

cd "$(dirname "$0")/.."
VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)}"
VERSION="${VERSION#v}"
OUT="dist"
APP="$OUT/TailConnect.app"
BUILD="$OUT/build-macos"
DMG="$OUT/TailConnect-$VERSION-macos.dmg"

rm -rf "$APP" "$BUILD" "$DMG" "$OUT/SHA256SUMS"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources" "$BUILD"

echo "==> Building universal binary ($VERSION)"
export CGO_ENABLED=1 MACOSX_DEPLOYMENT_TARGET=13.0
# The webview library's header trips a clang deprecation warning; harmless.
export CGO_CXXFLAGS="-Wno-deprecated-literal-operator"
LDFLAGS="-s -w -X main.version=$VERSION -extldflags=-Wl,-no_warn_duplicate_libraries"
for arch in arm64 amd64; do
  GOOS=darwin GOARCH=$arch go build -trimpath -ldflags "$LDFLAGS" -o "$BUILD/tailconnect-$arch" ./cmd/tailconnect
done
lipo -create -output "$APP/Contents/MacOS/tailconnect" "$BUILD/tailconnect-arm64" "$BUILD/tailconnect-amd64"
lipo -info "$APP/Contents/MacOS/tailconnect"

echo "==> Icon"
go run ./tools/mkicon "$BUILD/icons"
mkdir -p "$BUILD/AppIcon.iconset" && cp "$BUILD"/icons/icon_*.png "$BUILD/AppIcon.iconset/"
iconutil -c icns -o "$APP/Contents/Resources/AppIcon.icns" "$BUILD/AppIcon.iconset"

echo "==> Info.plist"
cat > "$APP/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>TailConnect</string>
  <key>CFBundleDisplayName</key><string>TailConnect</string>
  <key>CFBundleIdentifier</key><string>io.github.imbikramsaha.tailconnect</string>
  <key>CFBundleExecutable</key><string>tailconnect</string>
  <key>CFBundleIconFile</key><string>AppIcon</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>${VERSION%%-*}</string>
  <key>CFBundleVersion</key><string>$VERSION</string>
  <key>LSMinimumSystemVersion</key><string>13.0</string>
  <!-- Menu-bar app: no Dock icon, no app menu. -->
  <key>LSUIElement</key><true/>
  <key>NSHighResolutionCapable</key><true/>
  <key>NSHumanReadableCopyright</key><string>Copyright © 2026 TailConnect contributors. Apache License 2.0.</string>
  <key>NSAppleEventsUsageDescription</key><string>TailConnect opens Terminal or iTerm to start SSH sessions on your devices.</string>
  <key>NSLocalNetworkUsageDescription</key><string>TailConnect connects to your devices on the local network and over Tailscale.</string>
</dict>
</plist>
EOF

echo "==> Signing"
if [[ -n "${SIGN_IDENTITY:-}" ]]; then
  codesign --force --options runtime --timestamp -s "$SIGN_IDENTITY" "$APP"
else
  codesign --force -s - "$APP"
fi
codesign --verify --strict "$APP"

echo "==> DMG"
STAGE="$BUILD/dmg"
mkdir -p "$STAGE"
cp -R "$APP" "$STAGE/"
ln -s /Applications "$STAGE/Applications"
hdiutil create -quiet -volname "TailConnect" -srcfolder "$STAGE" -ov -format UDZO "$DMG"
if [[ -n "${SIGN_IDENTITY:-}" ]]; then
  codesign --force --timestamp -s "$SIGN_IDENTITY" "$DMG"
  if [[ -n "${NOTARY_PROFILE:-}" ]]; then
    echo "==> Notarizing"
    xcrun notarytool submit "$DMG" --keychain-profile "$NOTARY_PROFILE" --wait
    xcrun stapler staple "$DMG"
  fi
fi
(cd "$OUT" && shasum -a 256 "$(basename "$DMG")" > SHA256SUMS)
rm -rf "$BUILD"
echo
echo "Built $APP"
echo "      $DMG ($(du -h "$DMG" | cut -f1))"
echo "      $OUT/SHA256SUMS"
