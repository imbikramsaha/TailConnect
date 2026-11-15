#!/usr/bin/env bash
# Sign and publish a draft release created by CI.
#
#   scripts/publish-release.sh v0.2.1 [path/to/release.key]
#
# Downloads the draft's SHA256SUMS, shows it, signs it with the offline
# release key (default ~/.tailconnect-release/release.key), uploads
# SHA256SUMS.sig and publishes the release. Installed apps refuse updates
# without a valid signature, so CI alone can never ship an update.
set -euo pipefail
cd "$(dirname "$0")/.."
TAG="${1:?usage: publish-release.sh <tag> [key]}"
KEY="${2:-$HOME/.tailconnect-release/release.key}"
REPO="imbikramsaha/TailConnect"
[[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || { echo "bad tag $TAG" >&2; exit 1; }
[[ -f "$KEY" ]] || { echo "release key not found at $KEY" >&2; exit 1; }

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
draft=$(gh release view "$TAG" --repo "$REPO" --json isDraft --jq .isDraft)
[[ "$draft" == "true" ]] || { echo "$TAG is not a draft (already published?)" >&2; exit 1; }

echo "==> Downloading assets of draft $TAG to verify checksums locally"
gh release download "$TAG" --repo "$REPO" --dir "$WORK"
(cd "$WORK" && grep -v ' SHA256SUMS' SHA256SUMS | shasum -a 256 -c -)
echo "==> Checking build provenance (built by this repository's CI)"
for f in "$WORK"/TailConnect-* "$WORK"/tailconnect[-_]*; do
  [[ "$f" == *.rb ]] && continue
  gh attestation verify "$f" --repo "$REPO" >/dev/null || { echo "no valid provenance for $(basename "$f")" >&2; exit 1; }
  echo "   ok $(basename "$f")"
done
echo
cat "$WORK/SHA256SUMS"
echo
read -r -p "Sign and publish these files as $TAG? [y/N] " ok
[[ "$ok" == "y" || "$ok" == "Y" ]] || { echo "aborted"; exit 1; }

go run ./tools/relsign sign "$KEY" "$WORK/SHA256SUMS"
PUB=$(grep -A1 'var ReleaseKeys' internal/update/update.go | tail -1 | tr -d '\t",')
go run ./tools/relsign verify "$PUB" "$WORK/SHA256SUMS" "$WORK/SHA256SUMS.sig"
gh release upload "$TAG" --repo "$REPO" "$WORK/SHA256SUMS.sig"
gh release edit "$TAG" --repo "$REPO" --draft=false
echo "Published $TAG. Update the Homebrew tap with the release's tailconnect.rb."
