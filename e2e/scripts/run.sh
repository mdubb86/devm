#!/usr/bin/env bash
# run.sh — uv sync + pytest with crash-safe cleanup + signal escalation.
set -uo pipefail

# Resolve script dir BEFORE cd so we can source sweep.sh by absolute path.
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR/.."   # cd into e2e/

# Shared registry sweep used by EXIT trap.
# shellcheck source=./sweep.sh
source "$SCRIPT_DIR/sweep.sh"

export E2E_REGISTRY="$(mktemp -t devm-e2e-reg.XXXX)"

PYTEST_PID=""
shutdown() {
    echo "=== e2e: caught signal, terminating pytest ==="
    if [ -n "$PYTEST_PID" ]; then
        kill -TERM -- -"$PYTEST_PID" 2>/dev/null || true
        sleep 2
        kill -KILL -- -"$PYTEST_PID" 2>/dev/null || true
    fi
}
on_exit() {
    local rc=$?
    sweep_registry
    rm -f "$E2E_REGISTRY"
    exit $rc
}
trap shutdown INT TERM
trap on_exit EXIT

uv sync --quiet

# Build the devm binary into a temp location and export DEVM_BIN.
DEVM_BIN="${DEVM_BIN:-$(mktemp -d)/devm}"
(cd .. && go build -o "$DEVM_BIN" ./cmd/devm)
export DEVM_BIN

# Run pytest in its own process group so the shutdown trap can kill
# the whole tree on SIGINT/SIGTERM. `set -m` (monitor mode) is the
# macOS-portable equivalent of `setsid` — bash puts backgrounded
# processes into their own pgroup when job control is on.
set -m
uv run pytest "$@" &
PYTEST_PID=$!
set +m   # turn monitor mode back off so the trap doesn't see job-control noise
wait $PYTEST_PID
