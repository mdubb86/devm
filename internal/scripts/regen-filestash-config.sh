#!/usr/bin/env bash
# regen-filestash-config.sh — dev-machine one-off. Extracts the
# working filestash config + binary from the shelfmates project's
# running guest (which uses filestash's passthrough middleware, set
# up manually there), patches the connection root to "/" and the
# listen host/port to devm's fixed values, and writes both artifacts
# to internal/scripts/embed/.
#
# Rerun when bumping filestash: reconfigure shelfmates' filestash
# first (via its admin UI), then rerun this. Committed outputs are
# the sole shipped artifacts.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
EMBED_DIR="$REPO_ROOT/internal/scripts/embed"
SHELFMATES_DEVM="/Users/michael/.local/bin/devm"
SHELFMATES_DIR="/Users/michael/workspace/shelfmates"
SCRATCH="$(mktemp -d)"
trap 'rm -rf "$SCRATCH"' EXIT

echo "==> Extracting filestash binary + config from shelfmates guest"
cd "$SHELFMATES_DIR"
"$SHELFMATES_DEVM" exec -- cat /home/devm/filestash/filestash > "$SCRATCH/filestash"
chmod +x "$SCRATCH/filestash"
"$SHELFMATES_DEVM" exec -- cat /home/devm/filestash/data/state/config/config.json > "$SCRATCH/config.json"

echo "==> Patching config: connection root=/ + listen 0.0.0.0:8941"
python3 - <<PATCH
import json, sys
p = "$SCRATCH/config.json"
c = json.load(open(p))
# Force the connection root to / so filestash serves the whole guest fs.
assert c["connections"], "shelfmates config has no connections — reconfigure filestash there first"
c["connections"][0]["path"] = "/"
c["connections"][0]["type"] = "local"
c["connections"][0]["label"] = "local"
# Force our fixed bind (0.0.0.0:8941).
c.setdefault("general", {})
c["general"]["host"] = "0.0.0.0"
c["general"]["port"] = 8941
json.dump(c, open(p, "w"), indent=4)
PATCH

echo "==> Verifying patched config still has passthrough middleware"
python3 -c "
import json
c = json.load(open('$SCRATCH/config.json'))
assert c['middleware']['identity_provider']['type'] == 'passthrough', \
    'shelfmates config lost passthrough middleware — reconfigure there first'
assert c['connections'][0]['path'] == '/'
print('config validated')
"

echo "==> Writing embed artifacts"
cp "$SCRATCH/config.json" "$EMBED_DIR/filestash-config.json"
cp "$SCRATCH/filestash" "$EMBED_DIR/filestash"
chmod 755 "$EMBED_DIR/filestash"
ls -la "$EMBED_DIR/filestash-config.json" "$EMBED_DIR/filestash"
file "$EMBED_DIR/filestash" | grep -q 'ELF 64-bit LSB.*ARM aarch64' \
    || { echo "ERROR: filestash binary is not linux/arm64"; exit 1; }
echo "==> Done. Commit both artifacts."
