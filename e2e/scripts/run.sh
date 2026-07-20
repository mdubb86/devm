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
    rm -f "$E2E_REGISTRY"
    exit $rc
}
trap shutdown INT TERM
trap on_exit EXIT

uv sync --quiet

# Reap orphan e2e-* tart VMs from prior runs. Test fixtures name their
# VMs `e2e-<slug>-<hash>` and register them into E2E_REGISTRY for
# sweep_registry to delete on exit — but the registry only knows
# about VMs the CURRENT run created. A prior run that died before
# sweep_registry could fire (SIGKILL on bash, laptop sleep, CI job
# cancel) leaves its VMs behind, and they cost disk + occasionally
# hold shared resources (vmnet DHCP leases, tart's per-VM
# scratch dirs). Sweep them here so a fresh run starts from clean
# state.
#
# Only touches VMs prefixed `e2e-` — same allow-list the e2e test
# fixtures use and the same shape sweep-leftovers.sh matches. User
# VMs and `devm-base` are untouched. Silent when nothing's there.
ORPHAN_VMS=()
while read -r name; do
    [ -z "$name" ] && continue
    ORPHAN_VMS+=("$name")
done < <(tart list 2>/dev/null | awk 'NR>1 && $2 ~ /^e2e-/ {print $2}')
if [ "${#ORPHAN_VMS[@]}" -gt 0 ]; then
    echo "=== e2e: reaping ${#ORPHAN_VMS[@]} orphan e2e-* tart VM(s) ===" >&2
    for name in "${ORPHAN_VMS[@]}"; do
        tart stop "$name" >/dev/null 2>&1 || true
        tart delete "$name" >/dev/null 2>&1 || true
    done
fi

set -m
uv run pytest -p no:xdist "$@" &
PYTEST_PID=$!
set +m
wait $PYTEST_PID; rc=$?
[ $rc -eq 5 ] && rc=0
exit $rc
