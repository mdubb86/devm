#!/usr/bin/env bash
# scripts/install_test.sh — bash smoke for scripts/install.sh.
#
# Stands up a local file:// fake "releases API" and asserts install.sh
# fetches the right asset, verifies the checksum, and lays down a
# runnable devm binary.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
INSTALL_SH="$REPO_ROOT/scripts/install.sh"
FIXTURE_DIR="$REPO_ROOT/scripts/testdata"

# Generate release-fixture.json with absolute file:// URLs at test time
# (these can't be checked in — they depend on the local checkout path).
FIXTURE_JSON="$(mktemp)"
trap 'rm -f "$FIXTURE_JSON"' EXIT
cat > "$FIXTURE_JSON" <<EOF
[
  {
    "tag_name": "v0.1.0",
    "assets": [
      { "name": "devm_v0.1.0_darwin_arm64.tar.gz",
        "browser_download_url": "file://${FIXTURE_DIR}/devm_v0.1.0_darwin_arm64.tar.gz" },
      { "name": "checksums.txt",
        "browser_download_url": "file://${FIXTURE_DIR}/checksums.txt" }
    ]
  },
  {
    "tag_name": "recipes-abc1234",
    "assets": [
      { "name": "recipes.db",
        "browser_download_url": "file://${FIXTURE_DIR}/recipes.db" }
    ]
  }
]
EOF

DEST="$(mktemp -d)"
# Force darwin/arm64 so the smoke runs on any host (real Mac runs as-is;
# Linux CI runners use the overrides to test the jq + curl + checksum
# logic against the in-tree fixture).
DEVM_INSTALL_OS=darwin \
DEVM_INSTALL_ARCH=arm64 \
DEVM_RELEASES_URL="file://${FIXTURE_JSON}" \
DEVM_INSTALL_PREFIX="$DEST" \
    bash "$INSTALL_SH"

if [ ! -x "$DEST/devm" ]; then
    echo "FAIL: $DEST/devm not installed or not executable" >&2
    rm -rf "$DEST"
    exit 1
fi
if ! "$DEST/devm" version | grep -q "devm v0.1.0"; then
    echo "FAIL: installed binary didn't print the expected version" >&2
    rm -rf "$DEST"
    exit 1
fi
# iron-proxy is embedded in the devm binary (see internal/ironproxy/
# embed.go) — nothing to check on disk after install; the daemon
# extracts it on startup into ~/Library/Application Support/devm/bin/.
echo "PASS: install.sh fetched, verified, and installed v0.1.0"
rm -rf "$DEST"
