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

# Two-phase run:
#  1. tests NOT marked `pty` — parallel (pytest-xdist).
#  2. `pty`-marked tests — single process (`-p no:xdist`) because
#     pexpect's `pty.forkpty()` races on lock inheritance if the
#     process has a background xdist RPC thread.
#
# The two phases share the caller's other args ($REST_ARGS) but each
# adds its own `-m` expression. If the caller already passed `-m EXPR`
# we AND it with the phase filter.

# Extract caller's -m expression and their -n choice. Both are absorbed
# here (last -m wins in pytest anyway; and phase 2 forces -p no:xdist
# which would reject any leftover -n).
CALLER_MARK=""
REST_ARGS=()
skip_next=""
for arg in "$@"; do
    if [ -n "$skip_next" ]; then
        case "$skip_next" in
            mark) CALLER_MARK="$arg" ;;
            drop) : ;;
        esac
        skip_next=""
        continue
    fi
    case "$arg" in
        -m) skip_next="mark" ;;
        -n) skip_next="drop" ;;
        *)  REST_ARGS+=("$arg") ;;
    esac
done

parallel_mark="not pty"
serial_mark="pty"
if [ -n "$CALLER_MARK" ]; then
    parallel_mark="($CALLER_MARK) and not pty"
    serial_mark="($CALLER_MARK) and pty"
fi

rc_parallel=0
rc_serial=0

# Phase 1: parallel for non-pty tests. Capped at 4 workers — `-n auto`
# spawns one per CPU which overloads tart's guest-agent gRPC (surfaces as
# `Error: internal error (13): transport: SendHeader called multiple
# times` from concurrent tart exec calls).
set -m
uv run pytest -m "$parallel_mark" -n 4 ${REST_ARGS[@]+"${REST_ARGS[@]}"} &
PYTEST_PID=$!
set +m
wait $PYTEST_PID
rc_parallel=$?
# rc=5 = "no tests collected" — legitimate when a phase's marker
# intersection is empty (e.g. `-m contract` never intersects `pty`).
[ $rc_parallel -eq 5 ] && rc_parallel=0

# Phase 2: serial (no xdist) for pty tests. Skip pytest entirely when
# no pty-eligible tests are selected — spares us pytest startup cost
# on the parallel-only e2e-contract runs.
set -m
uv run pytest -m "$serial_mark" -p no:xdist ${REST_ARGS[@]+"${REST_ARGS[@]}"} &
PYTEST_PID=$!
set +m
wait $PYTEST_PID
rc_serial=$?
[ $rc_serial -eq 5 ] && rc_serial=0

if [ $rc_parallel -ne 0 ]; then
    exit $rc_parallel
fi
exit $rc_serial
