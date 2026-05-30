#!/usr/bin/env bash
# sweep-leftovers.sh — best-effort cleanup of anything an earlier run left
# behind. Use this if a prior run was killed harder than the wrapper could
# handle (e.g. SIGKILL on bash itself). NOT a normal-path tool.
set -uo pipefail

echo "=== e2e leftovers: stopped/running sandboxes named e2e-* ==="
mapfile -t SBOXES < <(sbx ls 2>/dev/null | awk 'NR>1 && $1 ~ /^e2e-/ {print $1}')
for s in "${SBOXES[@]}"; do
    echo "  sbx rm $s"
    sbx rm "$s" >/dev/null 2>&1 || true
done

echo "=== e2e leftovers: /tmp/devm-e2e-* and /private/tmp/devm-e2e-* ==="
rm -rf /tmp/devm-e2e-* /private/tmp/devm-e2e-* 2>/dev/null || true

echo "=== e2e leftovers: .invalid network policies ==="
mapfile -t POLS < <(sbx policy ls --type network 2>/dev/null | awk '/\.invalid/ {print $NF}')
for d in "${POLS[@]}"; do
    echo "  sbx policy rm network --resource $d"
    sbx policy rm network --resource "$d" >/dev/null 2>&1 || true
done
