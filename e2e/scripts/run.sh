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
# Co-locate iron-proxy next to it — the daemon looks next to its own
# executable to find iron-proxy (internal/ironproxy.Path). For the
# install/uninstall tests that register DEVM_BIN's path with launchd,
# the LaunchDaemon spawns from that temp dir; without a sibling
# iron-proxy the daemon fails /vm/start with a 500 (visible in
# ~/Library/Logs/com.devm.service.err.log as
# `iron-proxy adopt: locate iron-proxy: iron-proxy not found`).
# DEVM_BIN defaults to a stable path so the daemon's LaunchDaemon
# plist can keep pointing at the same location across runs. That
# enables the "skip reinstall if daemon already matches" check
# below — every avoided reinstall is one fewer Touch ID prompt.
# Override with DEVM_BIN=... in the env for CI / non-default layouts.
DEVM_BIN="${DEVM_BIN:-$HOME/.cache/devm-e2e/devm}"
DEVM_BIN_DIR="$(dirname "$DEVM_BIN")"
mkdir -p "$DEVM_BIN_DIR"

# Random per-build Fingerprint injected as a compiled-in constant.
# Both the CLI and the (installed) daemon share this value — they were
# built from the same `go build` invocation. Later, `devm` commands
# check the daemon's reported Fingerprint against their own (compiled-in)
# copy; a mismatch means the on-disk binary has been rebuilt since the
# daemon last started, and the CLI raises an error telling the user to
# `devm install`. Test infra uses that same signal to decide whether to
# reinstall — no bash-side hashing needed.
BUILD_FP="$(head -c 8 /dev/urandom | xxd -p)"
(cd .. && go build -ldflags "-X main.Fingerprint=$BUILD_FP" -o "$DEVM_BIN" ./cmd/devm)
if [ -x "$(cd .. && pwd)/bin/iron-proxy" ]; then
    cp "$(cd .. && pwd)/bin/iron-proxy" "$DEVM_BIN_DIR/iron-proxy"
fi
export DEVM_BIN

# No sudo priming or keepalive: on modern macOS with Touch ID, each
# privileged op (launchctl bootstrap, security add-trusted-cert, etc.)
# prompts individually anyway — sudo's timestamp cache doesn't skip
# biometric auth. So priming buys nothing. Commands that need sudo
# below (`devm uninstall`, `devm install`, and any install-marked
# test's own operations) will prompt when they need it. When the
# daemon already matches DEVM_BIN, nothing here needs sudo at all.

# Skip uninstall+install when the daemon is RUNNING and its
# Fingerprint matches. Two conditions:
#   - daemon.running == true (probe /health via unix socket)
#   - daemon.fingerprint_matches_cli == true (compare /version reply
#     to the CLI's compiled-in constant)
#
# We deliberately don't skip on "daemon down but on-disk binary
# matches" — the test suite needs the daemon actually serving, and
# starting it requires the same sudo/Touch ID launchctl op that
# reinstall does, so there's no saving to be had by trying to be
# clever about that case.
#
# `kardianos install` is a no-op when a plist already exists even
# for a different DEVM_BIN, so we can't rely on install alone —
# uninstall drops the plist so install writes a fresh one.
if "$DEVM_BIN" status --json 2>/dev/null | jq -e '.daemon.running == true and .daemon.fingerprint_matches_cli == true' >/dev/null 2>&1; then
    echo "=== e2e: daemon running and Fingerprint matches DEVM_BIN — skipping reinstall ===" >&2
    SKIP_INSTALL=1
else
    SKIP_INSTALL=0
    echo "=== e2e: uninstalling any prior devm daemon ===" >&2
    "$DEVM_BIN" uninstall >/dev/null 2>&1 || true
fi

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
# don't fail the run if pkill can't reach a PID. Silent when nothing's
# there to reap.
ORPHAN_IRON_PROXIES="$(pgrep -f 'iron-proxy -config .*/iron-proxy/.*\.yaml' 2>/dev/null | wc -l | tr -d ' ')"
if [ "${ORPHAN_IRON_PROXIES:-0}" -gt 0 ]; then
    echo "=== e2e: reaping $ORPHAN_IRON_PROXIES orphan iron-proxy process(es) ===" >&2
    pkill -f 'iron-proxy -config .*/iron-proxy/.*\.yaml' 2>/dev/null || true
fi

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

# Small settle so kernels release the port bindings before the fresh
# daemon starts picking ports. Only useful if we actually killed
# something above.
if [ "${ORPHAN_IRON_PROXIES:-0}" -gt 0 ] || [ "${#ORPHAN_VMS[@]}" -gt 0 ]; then
    sleep 1
fi
if [ "$SKIP_INSTALL" = "0" ]; then
    echo "=== e2e: installing devm daemon from $DEVM_BIN ===" >&2
    "$DEVM_BIN" install >/dev/null || {
        echo "=== e2e: devm install failed; see ~/Library/Logs/devm/install.log ===" >&2
        exit 1
    }
fi

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

# Single pytest run with -p no:xdist. Every just-target invokes run.sh
# with exactly one marker family (`-m devm and not install`,
# `-m install`, `-m contract`, `-m recipe`), each of which is either
# feature/pty tests OR install-lifecycle tests but never both. So
# there's no need to split into phases anymore — the old three-phase
# structure (parallel / touch-id / pty) existed for xdist reasons
# that no longer apply now that we're always serial.
#
# Serial (`-p no:xdist`) not parallel: we ran the suite at -n 4, -n 2,
# and fully serial. -n 4 and -n 2 both produced ~1 flake per run —
# different tests each time (test_43 SSL chain, test_52 state race,
# test_59 transport, test_68 VM died mid-provision). Serial produces
# zero flakes. Root cause: concurrent VM cold-starts on Apple
# Virtualization + tart-guest-agent contend for resources (memory,
# vmnet, guest-agent RPC channel) and occasionally kill each other.
set -m
uv run pytest -p no:xdist "$@" &
PYTEST_PID=$!
set +m
wait $PYTEST_PID
rc=$?
# rc=5 = "no tests collected" — legitimate for markers that don't
# match anything (e.g. `-m foo` where foo isn't a known marker).
[ $rc -eq 5 ] && rc=0
exit $rc
