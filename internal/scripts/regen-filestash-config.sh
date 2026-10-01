#!/usr/bin/env bash
# regen-filestash-config.sh — dev-machine one-off. Extracts the
# filestash binary + config.json from a RUNNING filestash guest
# whose admin console has already been walked through the preset
# "Local files - just for me". Writes both artifacts to
# internal/scripts/embed/. The committed config carries:
#   - auth.admin: a placeholder bcrypt hash. At bundle-render time
#     (render.RenderInstallScript) devm swaps this for bcrypt of the
#     project's own name, so each guest's filestash accepts its own
#     project name at the password prompt — users type their project
#     name on first visit per browser session.
#   - middleware.identity_provider.type = passthrough with the "just
#     for me" preset's encrypted params (encrypted with the pinned
#     SECRET_KEY so the blob is portable across every devm guest).
#   - connections[0] = {type: local, label: local} (no `path:` — the
#     preset leaves it unset; the SPA prompts the user through the
#     login flow).
#   - general.port = 8941, general.host = null (bind defaults to all
#     interfaces; filestash's advertised URL comes from the request's
#     Host header).
#
# Why the preset over hand-crafting: filestash's `identity_provider`
# params are AES-encrypted blobs tied to SECRET_KEY. Only the running
# filestash can produce a valid blob. The "just for me" preset gives
# the simplest shipping UX (one password field, no connection setup
# form).
#
# Regenerate manually (click-through):
#   1. In a running devm guest, visit filestash's admin console
#      (`/admin`), log in with the admin password, apply the
#      "Local files - just for me" configuration-wizard preset.
#   2. Re-point SHELFMATES_* below at the project you clicked
#      through in, then rerun this script.
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
c["general"]["host"] = None  # null = filestash uses the request's Host header for its advertised URL; must NOT be set to "0.0.0.0" (bind addr), which would render as the SPA's "Redirecting to http://0.0.0.0" bootscreen
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
