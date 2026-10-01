#!/usr/bin/env bash
# regen-filestash-config.sh — dev-machine one-off. Extracts the
# filestash binary + config.json from a RUNNING devm guest. Writes both
# to internal/scripts/embed/.
#
# What the committed config carries:
#   - middleware.identity_provider = passthrough with strategy=direct
#     (encrypted under general.secret_key). The direct strategy makes
#     filestash serve a zero-field self-posting form instead of a
#     password prompt, so a cold-browser visit to `/` auto-authenticates
#     and lands on the file listing.
#   - middleware.attribute_mapping = { related_backend: local, params
#     carries a literal admin password that the local backend bcrypt-
#     compares against auth.admin. Both MUST agree, so the baked
#     literal and the bcrypt of auth.admin's plaintext stay in sync.
#   - auth.admin = bcrypt of "devm". Only gates /admin; the main flow
#     bypasses it entirely under direct strategy.
#   - connections[0] = {type: local, label: local, path: "/"}.
#   - general.port = 8941, general.host = null (bind defaults to all
#     interfaces; filestash uses the request's Host header to decide
#     its advertised URL).
#
# Why the signed blobs: filestash's identity_provider / attribute_mapping
# params are AES-encrypted under general.secret_key. Only a running
# filestash can emit a valid blob — devm can't re-sign them at bundle-
# build time without reimplementing filestash's crypto. The committed
# SECRET_KEY ("WN3UuL2qn3rNEjmz") is deliberately shared across all devm
# guests so one guest's signed blob verifies in every other.
#
# This script re-flips the source guest's config to direct-strategy
# before extracting (idempotent — a guest already running direct stays
# direct), so a guest whose preset was walked through manually with a
# different strategy gets upgraded in place.
#
# Regenerate:
#   1. Spin up (or already have) a devm guest to extract from.
#   2. Re-point SOURCE_DEVM / SOURCE_DIR / SOURCE_TLD below at it.
#   3. Rerun this script.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
EMBED_DIR="$REPO_ROOT/internal/scripts/embed"

# Source guest. Shelfmates (prod devm, TLD "test") is the historical
# source; override to extract from an e2e-slot guest instead.
SOURCE_DEVM="${SOURCE_DEVM:-/Users/michael/.local/bin/devm}"
SOURCE_DIR="${SOURCE_DIR:-/Users/michael/workspace/shelfmates}"
SOURCE_TLD="${SOURCE_TLD:-test}"
PROJECT_NAME="$(basename "$SOURCE_DIR")"
FS_HOST="files.${PROJECT_NAME}.${SOURCE_TLD}"
SCRATCH="$(mktemp -d)"
trap 'rm -rf "$SCRATCH"' EXIT

echo "==> Flipping running filestash at https://${FS_HOST}/ to strategy=direct"
PY_FS_HOST="$FS_HOST" python3 - <<'FLIP'
import json
import os
import ssl
import sys
import urllib.request

HOST = "https://" + os.environ["PY_FS_HOST"]
PWD = "devm"  # must match the bcrypt plaintext we bake into auth.admin
ctx = ssl.create_default_context()  # devm's local CA is in the trust store


def _parse_set_cookie(header):
    out = {}
    if not header:
        return out
    name, _, rest = header.partition("=")
    val = rest.split(";", 1)[0]
    out[name] = val
    return out


def req(url, method="GET", data=None, cookies=None):
    headers = {"X-Requested-With": "XmlHttpRequest"}
    body = None
    if data is not None:
        headers["Content-Type"] = "application/json"
        body = json.dumps(data).encode()
    if cookies:
        headers["Cookie"] = "; ".join(f"{k}={v}" for k, v in cookies.items())
    r = urllib.request.Request(url, data=body, method=method, headers=headers)
    resp = urllib.request.urlopen(r, context=ctx)
    return resp.status, resp.read().decode(), _parse_set_cookie(resp.getheader("Set-Cookie"))


# Login.
status, body, cookies = req(f"{HOST}/admin/api/session", method="POST", data={"password": PWD})
assert status == 200 and json.loads(body).get("result") is True, f"login failed: {body}"
assert "admin" in cookies, f"no admin cookie in response: {cookies}"

# Fetch current config (plaintext — server decrypts params on read).
status, body, _ = req(f"{HOST}/admin/api/config", cookies=cookies)
assert status == 200, f"get config failed: {body}"
cfg = json.loads(body)["result"]

# Overwrite the two middleware blocks with the direct-strategy preset.
cfg["middleware"]["identity_provider"] = {
    "type": "passthrough",
    "params": json.dumps({"strategy": "direct"}),
}
cfg["middleware"]["attribute_mapping"] = {
    "related_backend": "local",
    "params": json.dumps({"local": {"type": "local", "password": PWD}}),
}

# POST modified config; server encrypts both params fields before writing.
status, body, _ = req(f"{HOST}/admin/api/config", method="POST", data=cfg, cookies=cookies)
assert status == 200 and json.loads(body).get("status") == "ok", f"save config failed: {body}"
print("    direct-strategy preset applied on the live guest")
FLIP

echo "==> Extracting filestash binary + (now-flipped) config"
cd "$SOURCE_DIR"
"$SOURCE_DEVM" exec -- bash -c 'cat /home/devm/filestash/filestash' > "$SCRATCH/filestash"
chmod +x "$SCRATCH/filestash"
"$SOURCE_DEVM" exec -- bash -c 'cat /home/devm/filestash/data/state/config/config.json' > "$SCRATCH/config.json"

echo "==> Patching connection path=/ and listen 0.0.0.0:8941"
python3 - <<PATCH
import json
p = "$SCRATCH/config.json"
c = json.load(open(p))
assert c["connections"], "extracted config has no connections"
c["connections"][0]["path"] = "/"
c["connections"][0]["type"] = "local"
c["connections"][0]["label"] = "local"
c.setdefault("general", {})
c["general"]["host"] = None  # null = filestash uses request Host for its advertised URL; NEVER "0.0.0.0"
c["general"]["port"] = 8941
json.dump(c, open(p, "w"), indent=4)
PATCH

echo "==> Verifying extracted config"
python3 -c "
import json
c = json.load(open('$SCRATCH/config.json'))
assert c['middleware']['identity_provider']['type'] == 'passthrough', 'identity_provider is not passthrough'
assert c['middleware']['attribute_mapping']['related_backend'] == 'local', 'attribute_mapping not bound to local'
assert c['connections'][0]['path'] == '/', 'connection path not patched'
assert c['general']['secret_key'] == 'WN3UuL2qn3rNEjmz', 'secret_key diverged from pinned value'
print('    config validated')
"

echo "==> Writing embed artifacts"
cp "$SCRATCH/config.json" "$EMBED_DIR/filestash-config.json"
cp "$SCRATCH/filestash" "$EMBED_DIR/filestash"
chmod 755 "$EMBED_DIR/filestash"
ls -la "$EMBED_DIR/filestash-config.json" "$EMBED_DIR/filestash"
file "$EMBED_DIR/filestash" | grep -q 'ELF 64-bit LSB.*ARM aarch64' \
    || { echo "ERROR: filestash binary is not linux/arm64"; exit 1; }
echo "==> Done. Commit both artifacts."
