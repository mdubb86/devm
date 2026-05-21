#!/usr/bin/env bash
# Claim VM-native volume mounts for the agent user (sbx remounts them
# root-owned on every restart).
set -euo pipefail

# Walk all ext4 mounts under $WORKSPACE_DIR and chown to agent.
mounts=$(findmnt -ln -t ext4 -o TARGET | grep -F "$WORKSPACE_DIR" || true)
if [ -z "$mounts" ]; then
  echo "ERROR: no ext4 volume mounts found under $WORKSPACE_DIR" >&2
  exit 0  # nothing to do; not fatal
fi
while read -r mount; do
  sudo chown -R agent:agent "$mount"
done <<< "$mounts"
