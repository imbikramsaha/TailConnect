#!/usr/bin/env bash
# Print the Homebrew cask for a built release:
#   scripts/update-cask.sh 0.2.0 > ../homebrew-tap/Casks/tailconnect.rb
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="${1:?version}"
DMG="dist/TailConnect-$VERSION-macos.dmg"
[[ -f "$DMG" ]] || { echo "missing $DMG (run scripts/build-macos.sh $VERSION)" >&2; exit 1; }
SHA=$(shasum -a 256 "$DMG" | cut -d' ' -f1)
sed -e "s/\"VERSION\"/\"$VERSION\"/" -e "s/\"SHA256\"/\"$SHA\"/" packaging/homebrew/tailconnect.rb
