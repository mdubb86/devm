#!/usr/bin/env bash
# scripts/install.sh — curl-installable devm host installer.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/mdubb86/devm/main/scripts/install.sh | bash
#
# Env knobs:
#   DEVM_INSTALL_PREFIX   override install dir (default: ~/.local/bin
#                          if writable, else /usr/local/bin)
#   DEVM_INSTALL_VERSION  install a specific vX.Y.Z (default: latest stable)
#   DEVM_RELEASES_URL     override the releases API endpoint (for tests)

set -euo pipefail

REPO="mdubb86/devm"
RELEASES_URL="${DEVM_RELEASES_URL:-https://api.github.com/repos/${REPO}/releases}"

die() { echo "install: $*" >&2; exit 1; }
log() { echo "install: $*" >&2; }

# OS + arch: real values from uname unless overridden (smoke tests
# on Linux CI runners use the overrides to pretend they're a Mac).
OS="${DEVM_INSTALL_OS:-$(uname -s | tr '[:upper:]' '[:lower:]')}"
case "$OS" in
    darwin) ;;
    *) die "unsupported OS: $OS (devm currently ships darwin-only)";;
esac
ARCH="${DEVM_INSTALL_ARCH:-$(uname -m)}"
case "$ARCH" in
    arm64|aarch64) ARCH=arm64;;
    x86_64|amd64)  ARCH=amd64;;
    *) die "unsupported arch: $ARCH";;
esac

PREFIX="${DEVM_INSTALL_PREFIX:-}"
if [ -z "$PREFIX" ]; then
    if [ -d "$HOME/.local/bin" ] || mkdir -p "$HOME/.local/bin" 2>/dev/null; then
        PREFIX="$HOME/.local/bin"
    elif [ -w /usr/local/bin ]; then
        PREFIX=/usr/local/bin
    else
        die "no writable install dir found (try DEVM_INSTALL_PREFIX=...)"
    fi
fi
mkdir -p "$PREFIX"

log "fetching releases from $RELEASES_URL"
RELEASES_JSON="$(curl -fsSL "$RELEASES_URL")"

VERSION="${DEVM_INSTALL_VERSION:-}"
if [ -z "$VERSION" ]; then
    VERSION="$(echo "$RELEASES_JSON" | jq -r \
        '[.[] | select(.tag_name | test("^v[0-9]+(\\.[0-9]+){0,2}$"))] | first | .tag_name')"
    if [ -z "$VERSION" ] || [ "$VERSION" = "null" ]; then
        die "no v* releases found at $RELEASES_URL"
    fi
fi
log "version: $VERSION"

ARCHIVE_NAME="devm_${VERSION}_darwin_${ARCH}.tar.gz"
ARCHIVE_URL="$(echo "$RELEASES_JSON" | jq -r \
    --arg name "$ARCHIVE_NAME" --arg tag "$VERSION" \
    '.[] | select(.tag_name == $tag) | .assets[] | select(.name == $name) | .browser_download_url')"
CHECKSUMS_URL="$(echo "$RELEASES_JSON" | jq -r \
    --arg tag "$VERSION" \
    '.[] | select(.tag_name == $tag) | .assets[] | select(.name == "checksums.txt") | .browser_download_url')"

if [ -z "$ARCHIVE_URL" ] || [ "$ARCHIVE_URL" = "null" ]; then
    die "no archive $ARCHIVE_NAME in release $VERSION"
fi
if [ -z "$CHECKSUMS_URL" ] || [ "$CHECKSUMS_URL" = "null" ]; then
    die "no checksums.txt in release $VERSION"
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
log "downloading $ARCHIVE_NAME"
curl -fsSL "$ARCHIVE_URL" -o "$TMP/$ARCHIVE_NAME"
log "downloading checksums.txt"
curl -fsSL "$CHECKSUMS_URL" -o "$TMP/checksums.txt"

(
    cd "$TMP"
    grep " $ARCHIVE_NAME$" checksums.txt > expected.txt \
        || die "checksum line for $ARCHIVE_NAME missing"
    shasum -a 256 -c expected.txt >/dev/null \
        || die "checksum verification failed"
)
log "checksum ok"

tar -xzf "$TMP/$ARCHIVE_NAME" -C "$TMP"
[ -x "$TMP/devm" ] || die "archive did not contain devm binary"
install -m 0755 "$TMP/devm" "$PREFIX/devm"
log "installed $PREFIX/devm"

if ! command -v devm >/dev/null 2>&1; then
    log "WARNING: devm is not on PATH — add $PREFIX to PATH or run $PREFIX/devm directly"
fi
"$PREFIX/devm" version
