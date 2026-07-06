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
SUDO_KEEPALIVE_PID=""
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
    if [ -n "$SUDO_KEEPALIVE_PID" ]; then
        kill "$SUDO_KEEPALIVE_PID" 2>/dev/null || true
    fi
    sweep_registry
    rm -f "$E2E_REGISTRY"
    exit $rc
}
trap shutdown INT TERM
trap on_exit EXIT

uv sync --quiet

# Build the devm binary into a temp location and export DEVM_BIN.
# Co-locate iron-proxy next to it — the daemon looks next to its own
# executable to find iron-proxy (internal/ironproxy.Path). For the
# install/uninstall tests that register DEVM_BIN's path with launchd,
# the LaunchDaemon spawns from that temp dir; without a sibling
# iron-proxy the daemon fails /vm/start with a 500 (visible in
# ~/Library/Logs/com.devm.service.err.log as
# `iron-proxy adopt: locate iron-proxy: iron-proxy not found`).
DEVM_BIN="${DEVM_BIN:-$(mktemp -d)/devm}"
DEVM_BIN_DIR="$(dirname "$DEVM_BIN")"
(cd .. && go build -o "$DEVM_BIN" ./cmd/devm)
if [ -x "$(cd .. && pwd)/bin/iron-proxy" ]; then
    cp "$(cd .. && pwd)/bin/iron-proxy" "$DEVM_BIN_DIR/iron-proxy"
fi
export DEVM_BIN

# Single up-front daemon install. Every devm-marked test needs the
# LaunchDaemon in-sync with DEVM_BIN, and doing it once here — before
# any pytest worker starts — avoids the pathological case of 4 xdist
# workers racing on `devm install` in their per-test autouse fixture
# (which serializes on the plist file and blows the 120s per-test
# timeout during setup).
#
# The autouse fixture becomes a verify-only safety net: if it ever
# sees a mismatch after this pre-install, the test suite aborts
# immediately with an actionable message.
if ! sudo -n true 2>/dev/null; then
    echo "=== e2e: sudo cache is cold — prime with 'sudo -v' first ===" >&2
    exit 2
fi

# Keep sudo alive for the whole run. Default cache is 5 min inactivity;
# the serial phase's install/uninstall tests run further in than that
# and otherwise hit `_require_sudo_primed()` mid-suite. The keepalive
# runs `sudo -n true` every 60s, which just refreshes the cache —
# never prompts (if the cache ever DID expire, -n makes it exit 1
# rather than prompt, and the next test failing loud is the correct
# signal). Killed at on_exit.
( while true; do sudo -n true 2>/dev/null || exit; sleep 60; done ) &
SUDO_KEEPALIVE_PID=$!

# Always uninstall + reinstall. `kardianos install` is a no-op when a
# plist already exists (even from a stale prior-session temp path), so
# we can't rely on the install command alone to switch the daemon over
# to the current DEVM_BIN. Uninstall drops the plist; install writes a
# fresh one pointing at DEVM_BIN.
echo "=== e2e: uninstalling any prior devm daemon ===" >&2
"$DEVM_BIN" uninstall >/dev/null 2>&1 || true
echo "=== e2e: installing devm daemon from $DEVM_BIN ===" >&2
"$DEVM_BIN" install >/dev/null || {
    echo "=== e2e: devm install failed; see ~/Library/Logs/devm/install.log ===" >&2
    exit 1
}

# Verify what we ended up with — if the daemon didn't actually pick up
# the new binary, bail immediately with concrete evidence, rather than
# letting the autouse fixture surface it later per-test.
DAEMON_PROG="$(launchctl print system/com.devm.service 2>/dev/null | awk -F'= ' '/^[[:space:]]*program = /{print $2; exit}')"
if [ "$DAEMON_PROG" != "$DEVM_BIN" ]; then
    echo "=== e2e: daemon didn't switch to DEVM_BIN after reinstall ===" >&2
    echo "    DEVM_BIN:            $DEVM_BIN" >&2
    echo "    daemon program path: $DAEMON_PROG" >&2
    exit 1
fi

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

parallel_mark="not pty and not serial"
serial_mark="pty or serial"
if [ -n "$CALLER_MARK" ]; then
    parallel_mark="($CALLER_MARK) and not pty and not serial"
    serial_mark="($CALLER_MARK) and (pty or serial)"
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
