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
        echo "=== sweep_registry: cleaning leaked sandboxes/workspaces ==="
        while IFS=$'\t' read -r sb ws; do
            [ -z "$sb" ] && continue
            echo "  sbx rm $sb  +  rm -rf $ws"
            sbx rm "$sb" >/dev/null 2>&1 || true
            rm -rf "$ws"   >/dev/null 2>&1 || true
        done < "$E2E_REGISTRY"
    fi
    rm -f "$E2E_REGISTRY"
    exit $exit_code
}
trap sweep_registry EXIT INT TERM

# Run the one test.
expect "$TEST"
