#!/usr/bin/env bash
# run.sh — uv sync + pytest with crash-safe sweep.
#
# Preconditions:
#   - `just e2e-bootstrap` has been run (for non-install tests), OR
#   - Test invokes `devm-e2e install` inside its own body (for install
#     tests, which manage their own state).
# Preconditions are enforced by the invoking just recipe, not here.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR/.."

# shellcheck source=./sweep.sh
source "$SCRIPT_DIR/sweep.sh"

export E2E_REGISTRY="$(mktemp -t devm-e2e-reg.XXXX)"

PYTEST_PID=""
shutdown() {
    echo "=== e2e: caught signal, terminating pytest ===" >&2
    if [ -n "$PYTEST_PID" ]; then
        kill -TERM -- -"$PYTEST_PID" 2>/dev/null || true
        sleep 2
        kill -KILL -- -"$PYTEST_PID" 2>/dev/null || true
    fi
}
on_exit() {
    local rc=$?
    sweep_registry
    # Leave nothing behind regardless of how the run ended — VM strays,
    # e2e-slot processes (iron-proxy, softnet, shims), stale temp dirs.
    # The start-of-run call below covers whatever this one can't (e.g.
    # SIGKILL on bash itself).
    sweep_e2e_leftovers
    rm -f "$E2E_REGISTRY"
    exit $rc
}
trap shutdown INT TERM
trap on_exit EXIT

uv sync --quiet

# Start from a clean slate: whatever any prior run left behind — VMs,
# iron-proxy/softnet/shim processes, temp dirs — goes now. The registry
# sweep only knows about the CURRENT run's resources; this catches
# every run that died before its own on_exit could fire.
sweep_e2e_leftovers

set -m
uv run pytest -p no:xdist "$@" &
PYTEST_PID=$!
set +m
wait $PYTEST_PID; rc=$?
[ $rc -eq 5 ] && rc=0
exit $rc
