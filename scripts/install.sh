#!/bin/sh
# TailConnect installer for macOS and Linux.
#
#   curl -fsSL https://github.com/imbikramsaha/TailConnect/releases/latest/download/install.sh | sh
#
# Downloads the latest release for this computer, checks its SHA-256 against
# the release's SHA256SUMS and - when an OpenSSL with Ed25519 support is
# available - checks SHA256SUMS against TailConnect's release signing key,
# then installs:
#   macOS:  TailConnect.app in /Applications (or ~/Applications) + `tailconnect` command
#   Linux:  the .deb (apt) or .rpm (dnf/yum/zypper) package, or a user-local install
# Options (environment): TAILCONNECT_VERSION=0.3.3 to pin a version.
set -eu

REPO="imbikramsaha/TailConnect"
# Release signing key (also compiled into the app; see SECURITY.md).
PUBKEY="bBlRWaSOYVipG6PdclZi7adlxdc3UxN7JM+LXHjdT6w="
# Signature by that key over "tailconnect-release-v1\nselftest\n": proves the
# local openssl can verify Ed25519 before trusting its verdict on a release.
SELFTEST_SIG="ukresXeaXD3tmSFJePmj5T3Gpnns26rmxnO+v9vQUWCiUkp2tQv55faN8LUS/+/KNSIc2blwGe8UdPmFWuKwAg=="

say() { printf '\033[1m%s\033[0m\n' "$*"; }
warn() { printf '\033[33m%s\033[0m\n' "$*" >&2; }
die() { printf '\033[31merror: %s\033[0m\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }

need curl
need uname
OS=$(uname -s)
ARCH=$(uname -m)
TMP=$(mktemp -d "${TMPDIR:-/tmp}/tailconnect.XXXXXX")
trap 'rm -rf "$TMP"' EXIT INT TERM

if [ -n "${TAILCONNECT_VERSION:-}" ]; then
  BASE="https://github.com/$REPO/releases/download/v${TAILCONNECT_VERSION#v}"
else
  BASE="https://github.com/$REPO/releases/latest/download"
fi

fetch() { # url dest
  curl -fsSL --proto '=https' --tlsv1.2 --retry 3 -o "$2" "$1" || die "download failed: $1"
}

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
  else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

# find_openssl prints an openssl that passes the Ed25519 self-test.
find_openssl() {
  printf '302a300506032b6570032100' | xxd -r -p > "$TMP/pub.der" 2>/dev/null || return 1
  printf '%s' "$PUBKEY" | base64 -d >> "$TMP/pub.der" 2>/dev/null || printf '%s' "$PUBKEY" | base64 -D >> "$TMP/pub.der" 2>/dev/null || return 1
  { echo "-----BEGIN PUBLIC KEY-----"; base64 < "$TMP/pub.der"; echo "-----END PUBLIC KEY-----"; } > "$TMP/pub.pem"
  printf 'tailconnect-release-v1\nselftest\n' > "$TMP/selftest.msg"
  printf '%s' "$SELFTEST_SIG" | base64 -d > "$TMP/selftest.sig" 2>/dev/null || printf '%s' "$SELFTEST_SIG" | base64 -D > "$TMP/selftest.sig"
  for o in openssl /opt/homebrew/opt/openssl@3/bin/openssl /usr/local/opt/openssl@3/bin/openssl; do
    command -v "$o" >/dev/null 2>&1 || continue
    if "$o" pkeyutl -verify -pubin -inkey "$TMP/pub.pem" -rawin -in "$TMP/selftest.msg" -sigfile "$TMP/selftest.sig" >/dev/null 2>&1; then
      echo "$o"
      return 0
    fi
  done
  return 1
}

# verify <file>: checksum (always) and release signature (when possible).
verify() {
  file="$1"
  name=$(basename "$file")
  want=$(awk -v n="$name" '$2 == n || $2 == "*"n {print $1}' "$TMP/SHA256SUMS")
  [ -n "$want" ] || die "$name is not listed in the release checksums"
  got=$(sha256 "$file")
  [ "$got" = "$want" ] || die "checksum mismatch for $name"
  if OSSL=$(find_openssl); then
    { printf 'tailconnect-release-v1\n'; cat "$TMP/SHA256SUMS"; } > "$TMP/sums.msg"
    base64 -d < "$TMP/SHA256SUMS.sig" > "$TMP/sums.sig" 2>/dev/null || base64 -D < "$TMP/SHA256SUMS.sig" > "$TMP/sums.sig"
    "$OSSL" pkeyutl -verify -pubin -inkey "$TMP/pub.pem" -rawin -in "$TMP/sums.msg" -sigfile "$TMP/sums.sig" >/dev/null 2>&1 ||
      die "release signature is NOT valid - refusing to install"
    say "Checksum and release signature verified"
  else
    say "Checksum verified"
    warn "  (install OpenSSL 3 to also verify the release signature)"
  fi
}

say "Downloading TailConnect for $OS/$ARCH..."
fetch "$BASE/SHA256SUMS" "$TMP/SHA256SUMS"
fetch "$BASE/SHA256SUMS.sig" "$TMP/SHA256SUMS.sig"

case "$OS" in
Darwin)
  fetch "$BASE/TailConnect-macos.dmg" "$TMP/TailConnect-macos.dmg"
  verify "$TMP/TailConnect-macos.dmg"
  MNT="$TMP/mnt"
  mkdir -p "$MNT"
  # macOS 27 deprecates `hdiutil attach` in favour of `diskutil image`, which
  # older systems don't have.
  if diskutil image attach --help >/dev/null 2>&1; then
    diskutil image attach --mountOptions nobrowse --readOnly --mountPoint "$MNT" "$TMP/TailConnect-macos.dmg" >/dev/null
  else
    hdiutil attach -nobrowse -readonly -noautoopen -mountpoint "$MNT" "$TMP/TailConnect-macos.dmg" >/dev/null
  fi
  DEST=/Applications
  [ -w "$DEST" ] || { DEST="$HOME/Applications"; mkdir -p "$DEST"; }
  if pgrep -xq tailconnect 2>/dev/null; then
    "$DEST/TailConnect.app/Contents/MacOS/tailconnect" quit >/dev/null 2>&1 || true
    sleep 1
  fi
  rm -rf "$DEST/TailConnect.app"
  ditto "$MNT/TailConnect.app" "$DEST/TailConnect.app"
  diskutil eject "$MNT" >/dev/null 2>&1 || hdiutil detach -force "$MNT" >/dev/null 2>&1 || true
  BIN="$DEST/TailConnect.app/Contents/MacOS/tailconnect"
  for d in /usr/local/bin /opt/homebrew/bin "$HOME/.local/bin"; do
    if [ -d "$d" ] && [ -w "$d" ]; then ln -sf "$BIN" "$d/tailconnect" && LINK="$d/tailconnect" && break; fi
  done
  say "Installed $DEST/TailConnect.app"
  [ -n "${LINK:-}" ] && say "Command line: tailconnect (linked in $(dirname "$LINK"))"
  open "$DEST/TailConnect.app"
  say "TailConnect is starting - look for its icon in the menu bar."
  ;;
Linux)
  case "$ARCH" in
  x86_64 | amd64) DEB=amd64 RPM=x86_64 TAR=amd64 ;;
  aarch64 | arm64) DEB=arm64 RPM=aarch64 TAR=arm64 ;;
  *) die "unsupported architecture $ARCH (amd64 and arm64 are supported)" ;;
  esac
  SUDO=""
  [ "$(id -u)" -eq 0 ] || SUDO="sudo"
  if command -v apt-get >/dev/null 2>&1; then
    F="$TMP/tailconnect_linux_$DEB.deb"
    fetch "$BASE/tailconnect_linux_$DEB.deb" "$F"
    verify "$F"
    $SUDO apt-get install -y "$F"
  elif command -v dnf >/dev/null 2>&1 || command -v yum >/dev/null 2>&1 || command -v zypper >/dev/null 2>&1; then
    F="$TMP/tailconnect_linux_$RPM.rpm"
    fetch "$BASE/tailconnect_linux_$RPM.rpm" "$F"
    verify "$F"
    if command -v dnf >/dev/null 2>&1; then $SUDO dnf install -y "$F"
    elif command -v yum >/dev/null 2>&1; then $SUDO yum install -y "$F"
    else $SUDO zypper --non-interactive install --allow-unsigned-rpm "$F"; fi
  else
    # Any other distribution: user-local install, no root needed.
    F="$TMP/tailconnect_linux_$TAR.tar.gz"
    fetch "$BASE/tailconnect_linux_$TAR.tar.gz" "$F"
    verify "$F"
    mkdir -p "$HOME/.local/bin" "$HOME/.local/share/applications" "$HOME/.local/share/icons/hicolor/256x256/apps"
    tar -xzf "$F" -C "$TMP"
    install -m 0755 "$TMP/tailconnect" "$TMP/tailconnect-window" "$HOME/.local/bin/"
    install -m 0644 "$TMP/tailconnect.png" "$HOME/.local/share/icons/hicolor/256x256/apps/"
    sed "s|^Exec=tailconnect|Exec=$HOME/.local/bin/tailconnect|" "$TMP/tailconnect.desktop" > "$HOME/.local/share/applications/tailconnect.desktop"
    say "Installed to ~/.local/bin (make sure it's on your PATH)"
  fi
  say "TailConnect installed. Start it from your applications menu, or run: tailconnect"
  ;;
*)
  die "unsupported system $OS - on Windows run: irm https://github.com/$REPO/releases/latest/download/install.ps1 | iex"
  ;;
esac
