#!/usr/bin/env bash
# Single per-sandbox-startup script. Runs as wrapped startup step 1
# under the install user (UID 1000), so sudo is available for the
# operations that need it.
#
# Responsibilities (all idempotent — re-running on restart is safe):
#
#   1. Claim VM-native volume mounts for the agent user. Sbx remounts
#      every ext4 volume root-owned on each restart, so install-time
#      chown alone is not sufficient — only this re-chown survives.
#   2. Sync .devm/Caddyfile → /etc/caddy/Caddyfile. Reload if Caddy is
#      already running; otherwise start it in the background. No-op if
#      caddy isn't installed in this sandbox.
#   3. Sync .devm/hosts.fragment → /etc/hosts, splicing between
#      BEGIN/END devm hostnames markers so unrelated entries survive.
#      Empty fragment removes the block entirely.
set -euo pipefail

# 1. Chown VM-native volumes (one per mask). findmnt may return empty
#    when the project has no masks: declared — that's fine, not fatal.
mounts=$(findmnt -ln -t ext4 -o TARGET | grep -F "$WORKSPACE_DIR" || true)
if [ -n "$mounts" ]; then
  while read -r mount; do
    sudo chown -R agent:agent "$mount"
  done <<< "$mounts"
fi

# 2. Caddy: sync the rendered Caddyfile and ensure caddy is running.
#    Reverse-proxies in-VM service ports for hostname-based routing.
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

# 3. /etc/hosts: splice the rendered fragment between BEGIN/END markers
#    so service hostnames resolve to loopback (where caddy listens).
#    Empty fragment strips the block.
if [ -f "$WORKSPACE_DIR/.devm/hosts.fragment" ]; then
  tmp=$(mktemp)
  awk '
    /^# BEGIN devm hostnames$/ { skip = 1; next }
    /^# END devm hostnames$/   { skip = 0; next }
    !skip { print }
  ' /etc/hosts > "$tmp"
  if [ -s "$WORKSPACE_DIR/.devm/hosts.fragment" ]; then
    {
      echo "# BEGIN devm hostnames"
      cat "$WORKSPACE_DIR/.devm/hosts.fragment"
      echo "# END devm hostnames"
    } >> "$tmp"
  fi
  if ! sudo cmp -s "$tmp" /etc/hosts 2>/dev/null; then
    sudo cp "$tmp" /etc/hosts
  fi
  rm -f "$tmp"
fi
