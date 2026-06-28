#!/usr/bin/env bash
# sweep-leftovers.sh — best-effort cleanup of anything an earlier run left
# behind. Use this if a prior run was killed harder than the wrapper could
# handle (e.g. SIGKILL on bash itself). NOT a normal-path tool.
set -uo pipefail

echo "=== e2e leftovers: tart VMs named e2e-* ==="
mapfile -t SBOXES < <(tart list 2>/dev/null | awk 'NR>1 && $2 ~ /^e2e-/ {print $2}')
for s in "${SBOXES[@]}"; do
    echo "  tart delete $s"
    tart delete "$s" >/dev/null 2>&1 || true
done

echo "=== e2e leftovers: /tmp/devm-e2e-* and /private/tmp/devm-e2e-* ==="
rm -rf /tmp/devm-e2e-* /private/tmp/devm-e2e-* 2>/dev/null || true
