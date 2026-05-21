#!/usr/bin/env bash
# devm provisioning script — runs ONCE at sandbox creation (kit commands.install).
set -euo pipefail

cd "$WORKSPACE_DIR"

echo "==> [1/4] Node.js 24 (current LTS)"
curl -fsSL https://deb.nodesource.com/setup_24.x | sudo -E bash - > /dev/null
sudo apt-get install -y -qq nodejs > /dev/null
node --version | grep -q '^v24' \
  || { echo "ERROR: expected node v24, got $(node --version)" >&2; exit 1; }

echo "==> [2/4] Claude Code"
curl -fsSL https://claude.ai/install.sh | bash
command -v claude >/dev/null || [ -x "$HOME/.local/bin/claude" ] \
  || { echo "ERROR: claude binary not found after install" >&2; exit 1; }

echo "==> [3/4] pnpm via corepack"
sudo corepack enable
command -v pnpm >/dev/null \
  || { echo "ERROR: pnpm shim missing after corepack enable" >&2; exit 1; }

echo "==> [4/4] Caddy + hostname routing"
if ! command -v caddy >/dev/null; then
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
    | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
    | sudo tee /etc/apt/sources.list.d/caddy-stable.list > /dev/null
  sudo apt-get update -qq > /dev/null
  sudo apt-get install -y -qq caddy > /dev/null
fi

# /etc/hosts entries are populated by devm-exec.sh on every exec (idempotent),
# so newly-added hostnames in devm.yaml flow through without re-provisioning.

echo "==> provision complete"
