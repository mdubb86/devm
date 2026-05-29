#!/usr/bin/env bash
# run-one.sh <test.exp>
#
# Wrapper for running a single test with the same cleanup discipline
# as run-all.sh (two-layer registry sweep on EXIT/INT/TERM). Use this
# instead of bare `expect <file>` so leaks get caught even if the test
# crashes or you ^C mid-run.
set -uo pipefail

if [ $# -lt 1 ]; then
    echo "usage: $0 <test.exp>" >&2
    exit 2
fi

TEST="$1"
cd "$(dirname "$0")/../.."   # repo root

# Build devm (cheap on rebuilds).
DEVM_BIN="${DEVM_BIN:-$(mktemp -d)/devm}"
if ! go build -o "$DEVM_BIN" ./cmd/devm; then
    echo "BUILD FAILED" >&2
    exit 1
fi
export DEVM_BIN

# Registry + EXIT trap.
export E2E_REGISTRY="$(mktemp -t devm-e2e-registry.XXXX)"
sweep_registry() {
    local exit_code=$?
    if [ -s "$E2E_REGISTRY" ]; then
        echo ""
        echo "=== sweep_registry: cleaning leaked sandboxes/workspaces/policies ==="
        while IFS=$'\t' read -r a b; do
            [ -z "$a" ] && continue
            if [ "$a" = "policy" ]; then
                echo "  sbx policy rm network --resource $b"
                sbx policy rm network --resource "$b" >/dev/null 2>&1 || true
            else
                echo "  sbx rm $a  +  rm -rf $b"
                sbx rm "$a" >/dev/null 2>&1 || true
                rm -rf "$b"  >/dev/null 2>&1 || true
            fi
        done < "$E2E_REGISTRY"
    fi
    rm -f "$E2E_REGISTRY"
    exit $exit_code
}
trap sweep_registry EXIT INT TERM

# Run the one test, reporting wall-clock duration.
_start=$(date +%s)
expect "$TEST"
_rc=$?
_elapsed=$(( $(date +%s) - _start ))
printf '\n--- %s: %ds ---\n' "$(basename "$TEST")" "$_elapsed"
exit $_rc
