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
# and otherwise hit `_require_sudo_primed()` mid-suite.
#
# `sudo -n -v` explicitly refreshes the credential cache TIMESTAMP
# (`-v`) without prompting on failure (`-n`). Plain `sudo -n true`
# uses the cache but doesn't reliably bump the timestamp on macOS,
# so the cache still expires after 5 min even with the keepalive
# running.
#
# If the cache goes cold anyway (backgrounded shell, laptop sleep,
# etc.), the keepalive exits — the next test hitting sudo will fail
# loud rather than silently prompt in a way the user might miss
# (e.g., a Touch ID dialog blocked by a stuck stdin).
( while true; do sudo -n -v 2>/dev/null || exit; sleep 60; done ) &
SUDO_KEEPALIVE_PID=$!

# Always uninstall + reinstall. `kardianos install` is a no-op when a
# plist already exists (even from a stale prior-session temp path), so
# we can't rely on the install command alone to switch the daemon over
# to the current DEVM_BIN. Uninstall drops the plist; install writes a
# fresh one pointing at DEVM_BIN.
echo "=== e2e: uninstalling any prior devm daemon ===" >&2
"$DEVM_BIN" uninstall >/dev/null 2>&1 || true

# Reap orphan iron-proxies from prior test runs. `devm uninstall`
# doesn't cascade to iron-proxy children (they setsid'd on spawn to
# survive daemon death by design), so every test-suite run
# accumulates more of them and each holds a MAC_HOST:port binding.
# Eventually pickPort in a fresh /vm/start collides with one of those
# ports, iron-proxy fails to bind, and cold-start fails (typically
# hitting test_44 or another iron-proxy-lifecycle test).
#
# Match on `/iron-proxy -config .*/iron-proxy/*.yaml` — the argv
# pattern the daemon always uses (see internal/serviceapi/ironproxy.go
# SpawnIronProxy). Never matches the user's shell or tart. Best-effort;
# don't fail the run if pkill can't reach a PID.
echo "=== e2e: reaping orphan iron-proxy processes ===" >&2
pkill -f 'iron-proxy -config .*/iron-proxy/.*\.yaml' 2>/dev/null || true
# Small settle so kernels release the port bindings before the fresh
# daemon starts picking ports.
sleep 1
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
sudo_mark="serial"
pty_mark="pty and not serial"
if [ -n "$CALLER_MARK" ]; then
    parallel_mark="($CALLER_MARK) and not pty and not serial"
    sudo_mark="($CALLER_MARK) and serial"
    pty_mark="($CALLER_MARK) and pty and not serial"
fi

rc_parallel=0
rc_sudo=0
rc_pty=0

# Phase 1: serial (`-p no:xdist`). We ran the suite at -n 4, -n 2, and
# fully serial. -n 4 and -n 2 both produce ~1 flake per run — different
# tests each time (test_43 SSL chain, test_52 state race, test_59
# transport, test_68 VM died mid-provision). Serial produces zero
# flakes across the same suite. Root cause: concurrent VM cold-starts
# on Apple Virtualization + tart-guest-agent contend for resources
# (memory, vmnet, guest-agent RPC channel) and occasionally kill each
# other. Trade-off: phase 1 grows from ~90s to ~6 min. Full suite is
# ~14 min. Worth it for a deterministic green.
set -m
uv run pytest -m "$parallel_mark" -p no:xdist ${REST_ARGS[@]+"${REST_ARGS[@]}"} &
PYTEST_PID=$!
set +m
wait $PYTEST_PID
rc_parallel=$?
# rc=5 = "no tests collected" — legitimate when a phase's marker
# intersection is empty (e.g. `-m contract` never intersects `pty`).
[ $rc_parallel -eq 5 ] && rc_parallel=0

# Phase 2a: serial-marked (install/uninstall/service-restart) tests
# FIRST. These fire macOS Touch ID prompts every time they invoke sudo
# on privileged operations (security add-trusted-cert, launchctl
# bootstrap, etc.) — even with a warm sudo timestamp. Grouping them
# up front means the prompts happen in a single burst while the user
# is watching, not scattered through a 10-minute run.
echo "=== e2e: phase 2a — sudo/touch-id tests (watch for prompts) ===" >&2
set -m
uv run pytest -m "$sudo_mark" -p no:xdist ${REST_ARGS[@]+"${REST_ARGS[@]}"} &
PYTEST_PID=$!
set +m
wait $PYTEST_PID
rc_sudo=$?
[ $rc_sudo -eq 5 ] && rc_sudo=0

# Between 2a and 2b: restore the daemon. Phase 2a's install/uninstall
# tests leave the host uninstalled at teardown (they no longer
# reinstall in their own finally blocks — one restore here costs one
# Touch ID prompt instead of one-per-test). Phase 2b's pty tests rely
# on `devm shell` which needs the daemon.
if [ $rc_sudo -eq 0 ]; then
    echo "=== e2e: restoring devm daemon after phase 2a (Touch ID) ===" >&2
    "$DEVM_BIN" install >/dev/null || {
        echo "=== e2e: post-phase-2a devm install failed ===" >&2
        exit 1
    }
fi

# Phase 2b: pty tests. Serial (no xdist) because pexpect's
# pty.forkpty() races on lock inheritance if the process has a
# background xdist RPC thread.
echo "=== e2e: phase 2b — pty tests ===" >&2
set -m
uv run pytest -m "$pty_mark" -p no:xdist ${REST_ARGS[@]+"${REST_ARGS[@]}"} &
PYTEST_PID=$!
set +m
wait $PYTEST_PID
rc_pty=$?
[ $rc_pty -eq 5 ] && rc_pty=0

if [ $rc_parallel -ne 0 ]; then
    exit $rc_parallel
fi
if [ $rc_sudo -ne 0 ]; then
    exit $rc_sudo
fi
exit $rc_pty
