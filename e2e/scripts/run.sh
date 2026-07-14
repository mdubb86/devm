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

# --- Isolated mode ---
# When E2E_ISOLATE=1 (set by the just targets that don't need to
# exercise install lifecycle: e2e-devm, e2e-contract, e2e-recipe,
# e2e-one), run a foreground `devm serve` in a private
# DEVM_RUNTIME_DIR under /tmp so we don't touch the user's real
# `~/Library/Application Support/devm/`. This is the fix for the
# recurring pain where running e2e trampled a real project's
# iron-proxy configs (`devm uninstall` wipes the runtime dir; the
# iron-proxy reaper below pkills every iron-proxy on the system).
#
# Isolated mode skips: `devm uninstall`, `devm install`, the orphan
# iron-proxy reap (isolated iron-proxies come and go with the private
# runtime dir), the launchctl-plist verification (there is no plist).
# It still runs the tart-VM orphan reap because those live outside
# the runtime dir.
if [ "${E2E_ISOLATE:-0}" = "1" ]; then
    ISOLATED_RUNTIME_DIR="$(mktemp -d -t devm-e2e-runtime.XXXX)"
    export DEVM_RUNTIME_DIR="$ISOLATED_RUNTIME_DIR"
    # Ephemeral DNS port so we don't collide with the real daemon on
    # :51153. E2e tests don't exercise Mac-side *.test resolution
    # (that lives in the install-marker group); iron-proxy's own
    # per-project DNS listeners are separate and already dynamic.
    export DEVM_DNS_ADDR="127.0.0.1:0"
    echo "=== e2e: isolated runtime dir at $DEVM_RUNTIME_DIR ===" >&2

    # Auto-rebuild devm-base BEFORE spawning the daemon, so any branch
    # that changed image/provision-base.sh gets a fresh image without
    # requiring `devm install`'s sudo path. Streams progress to stderr
    # (build takes 5-10min on a fresh pull). No-op when the image is
    # already current — cheap idempotent check.
    #
    # Explicit exit-code check: the surrounding script only has
    # `set -uo pipefail` (no -e), so a failed rebuild would otherwise
    # silently proceed to serve --foreground and every test would fail
    # obscurely at cold-start against a stale/missing devm-base.
    if ! "$DEVM_BIN" _build-base-if-needed; then
        echo "=== e2e: base image rebuild failed; aborting ===" >&2
        exit 1
    fi

    # Foreground daemon in the background of this script. Uses its own
    # socket (under $DEVM_RUNTIME_DIR/devm.sock) so it can't conflict
    # with the user's real launchd-managed daemon. Not a LaunchDaemon
    # — no sudo, no plist, no /etc/resolver/test written. Tests that
    # need Mac-side *.test resolution or ports 80/443 need install-
    # marker mode; the non-install suite doesn't.
    E2E_DAEMON_LOG="$ISOLATED_RUNTIME_DIR/daemon.log"
    "$DEVM_BIN" serve --foreground >"$E2E_DAEMON_LOG" 2>&1 &
    E2E_DAEMON_PID=$!

    # Wait up to 10s for the isolated daemon's /health to answer.
    _deadline=$(( $(date +%s) + 10 ))
    while [ "$(date +%s)" -lt "$_deadline" ]; do
        if "$DEVM_BIN" status --json 2>/dev/null | \
           jq -e '.daemon.running == true' >/dev/null 2>&1; then
            break
        fi
        sleep 0.2
    done
    if ! "$DEVM_BIN" status --json 2>/dev/null | \
         jq -e '.daemon.running == true' >/dev/null 2>&1; then
        echo "=== e2e: isolated daemon never came up; log:" >&2
        cat "$E2E_DAEMON_LOG" >&2
        exit 1
    fi

    # Extra teardown: kill the isolated daemon's iron-proxy children,
    # then the daemon, then rm its runtime dir. Redefine on_exit to
    # add these steps; the EXIT trap set at line 31 resolves the
    # function by name at fire time so this override wins.
    #
    # Iron-proxies are setsid'd on spawn to survive daemon death by
    # design (see internal/supervisor/setsid_darwin.go). Killing the
    # daemon alone leaves them running as orphans on their MAC_HOST
    # ports. Kill them explicitly BEFORE the daemon so tests can't
    # accidentally reuse a port on the next run.
    #
    # The pattern matches iron-proxies whose -config path is under our
    # isolated runtime dir. Cannot match the user's real project
    # iron-proxies — their configs live under a different runtime dir
    # entirely (~/Library/Application Support/devm/).
    on_exit() {
        local rc=$?
        pkill -TERM -f "iron-proxy -config ${ISOLATED_RUNTIME_DIR}" 2>/dev/null || true
        # Small grace for iron-proxy to exit before we take the daemon down.
        for _ in 1 2 3 4 5; do
            pgrep -f "iron-proxy -config ${ISOLATED_RUNTIME_DIR}" >/dev/null 2>&1 || break
            sleep 0.2
        done
        pkill -KILL -f "iron-proxy -config ${ISOLATED_RUNTIME_DIR}" 2>/dev/null || true

        kill -TERM "$E2E_DAEMON_PID" 2>/dev/null || true
        # Give the daemon a moment to shut down cleanly, then SIGKILL.
        for _ in 1 2 3 4 5; do
            if ! kill -0 "$E2E_DAEMON_PID" 2>/dev/null; then break; fi
            sleep 0.2
        done
        kill -KILL "$E2E_DAEMON_PID" 2>/dev/null || true

        rm -rf "$ISOLATED_RUNTIME_DIR"
        sweep_registry
        rm -f "$E2E_REGISTRY"
        exit "$rc"
    }

    SKIP_INSTALL=1  # rest of the script gates on this
fi

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
if [ "${E2E_ISOLATE:-0}" = "1" ]; then
    :  # already handled above
elif "$DEVM_BIN" status --json 2>/dev/null | jq -e '.daemon.running == true and .daemon.fingerprint_matches_cli == true' >/dev/null 2>&1; then
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
#
# Skipped in isolated mode: the reap pattern is system-wide and would
# also kill the user's real project iron-proxies. Isolated iron-proxies
# get cleaned up when the sandbox runtime dir is removed at teardown.
if [ "${E2E_ISOLATE:-0}" != "1" ]; then
    ORPHAN_IRON_PROXIES="$(pgrep -f 'iron-proxy -config .*/iron-proxy/.*\.yaml' 2>/dev/null | wc -l | tr -d ' ')"
    if [ "${ORPHAN_IRON_PROXIES:-0}" -gt 0 ]; then
        echo "=== e2e: reaping $ORPHAN_IRON_PROXIES orphan iron-proxy process(es) ===" >&2
        pkill -f 'iron-proxy -config .*/iron-proxy/.*\.yaml' 2>/dev/null || true
    fi
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

# Plist verification only applies to install-mode (launchd is running
# the daemon). Isolated mode's daemon is our own foreground `devm
# serve` — the daemon-up probe below is enough.
if [ "${E2E_ISOLATE:-0}" != "1" ]; then
    # Verify what we ended up with:
    #   1. Plist points at DEVM_BIN — catches "install said ok but the
    #      plist path is wrong" bugs.
    #   2. Daemon actually responds on its unix socket AND its Fingerprint
    #      matches — catches the zombie-daemon case where launchctl thinks
    #      the service is running but the socket file was deleted from
    #      under it (e.g., stale process holding an unlinked fd), or where
    #      launchctl's KeepAlive kept an old process alive across an
    #      install that should have replaced it.
    DAEMON_PROG="$(launchctl print system/com.devm.service 2>/dev/null | awk -F'= ' '/^[[:space:]]*program = /{print $2; exit}')"
    if [ "$DAEMON_PROG" != "$DEVM_BIN" ]; then
        echo "=== e2e: daemon didn't switch to DEVM_BIN after reinstall ===" >&2
        echo "    DEVM_BIN:            $DEVM_BIN" >&2
        echo "    daemon program path: $DAEMON_PROG" >&2
        exit 1
    fi
    # Wait up to 10s for the socket to accept connections AND for the
    # daemon Fingerprint to match. On a fresh install, launchctl kicks the
    # process asynchronously; give it a moment before failing.
    _deadline=$(( $(date +%s) + 10 ))
    while [ "$(date +%s)" -lt "$_deadline" ]; do
        if "$DEVM_BIN" status --json 2>/dev/null | \
           jq -e '.daemon.running == true and .daemon.fingerprint_matches_cli == true' >/dev/null 2>&1; then
            break
        fi
        sleep 0.5
    done
    if ! "$DEVM_BIN" status --json 2>/dev/null | \
         jq -e '.daemon.running == true and .daemon.fingerprint_matches_cli == true' >/dev/null 2>&1; then
        echo "=== e2e: daemon not reachable via socket after install ===" >&2
        echo "    plist says the service is running but the CLI can't dial the" >&2
        echo "    unix socket, or the daemon reports a Fingerprint that doesn't" >&2
        echo "    match this CLI. Most likely a zombie process holding an" >&2
        echo "    unlinked socket fd from a prior uninstall. Fix:" >&2
        echo "" >&2
        echo "    sudo launchctl bootout system/com.devm.service" >&2
        echo "    (then rerun this command)" >&2
        exit 1
    fi
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
