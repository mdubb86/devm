#!/usr/bin/env bash
# In-VM entrypoint for every devm-driven exec.
#
# Invoked via:
#   sbx exec -it -w "$PWD" "$SANDBOX" \
#     bash "$WORKSPACE_DIR/.devm/scripts/devm-exec.sh" <cmd> [args…]
set -e

# Make $HOME/.local/bin visible to non-interactive shells (claude lives there).
export PATH="$HOME/.local/bin:$PATH"

if [ -x "$WORKSPACE_DIR/.devm/scripts/init-volumes.sh" ]; then
  bash "$WORKSPACE_DIR/.devm/scripts/init-volumes.sh" >/dev/null
fi

# Sync rendered Caddyfile into /etc/caddy. cp + reload only if changed.
if command -v caddy >/dev/null && [ -f "$WORKSPACE_DIR/.devm/Caddyfile" ]; then
  if ! sudo cmp -s "$WORKSPACE_DIR/.devm/Caddyfile" /etc/caddy/Caddyfile 2>/dev/null; then
    sudo cp "$WORKSPACE_DIR/.devm/Caddyfile" /etc/caddy/Caddyfile
    if pgrep -x caddy >/dev/null; then
      sudo caddy reload --config /etc/caddy/Caddyfile >/dev/null 2>&1 || true
    fi
  fi
  if ! pgrep -x caddy >/dev/null; then
    sudo caddy start --config /etc/caddy/Caddyfile --pidfile /run/caddy.pid >/dev/null 2>&1 || true
  fi
fi

exec "$@"
