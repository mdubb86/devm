#!/usr/bin/env bash
# sweep.sh — crash-safe cleanup for the e2e slot.
# Sourced by run.sh and purge-leftovers.sh; also runnable standalone
# (define E2E_REGISTRY first for sweep_registry).

# sweep_registry removes resources the CURRENT run registered but whose
# fixtures never got to clean up (pytest SIGKILL, wedged worker).
sweep_registry() {
    [ -z "${E2E_REGISTRY:-}" ] && return 0
    [ -s "$E2E_REGISTRY" ] || return 0
    echo "=== e2e: sweeping leaked resources ==="
    while IFS=$'\t' read -r kind val; do
        [ -z "$kind" ] && continue
        case "$kind" in
            sandbox)
                echo "  tart delete $val"
                tart delete "$val" >/dev/null 2>&1 || true
                ;;
            workspace)
                echo "  rm -rf $val"
                rm -rf "$val" >/dev/null 2>&1 || true
                ;;
            *)
                echo "  (unknown kind: $kind)"
                ;;
        esac
    done < "$E2E_REGISTRY"
}

# purge_e2e_leftovers removes everything a PRIOR run (or a run that just
# ended, cleanly or not) left in the e2e slot: e2e-* tart VMs, every
# process whose argv references the e2e identity's runtime dir
# (iron-proxy, softnet, setsid shims, mutagen ssh — the survive-daemon-
# restart design means nothing else ever kills these once their
# project's teardown is missed), and stale e2e temp dirs.
#
# Scope guarantees:
#   - Only VMs named `e2e-*` (fixture naming, e2e/conftest.py
#     sandbox_name). User VMs and `devm-base` untouched.
#   - The process pattern is the e2e RuntimeDir path — it can never
#     match the prod slot (`.../devm/` vs `.../devm-e2e/`), the e2e
#     daemon itself (argv `/usr/local/bin/devm-e2e`), or the root
#     helper.
#   - Descendants of the currently-running `devm-e2e serve` daemon are
#     EXCLUDED — its `mutagen sync monitor` subprocess argv matches
#     the runtime-dir pattern but isn't a leftover, it's an in-flight
#     child. Reaping it would just make the daemon's own subscriber
#     reconnect a moment later and inflate the "leftover count".
#
# Known residue: a proxy the still-running e2e daemon tracks in live
# state gets respawned by its watchdog within ~30s of being killed
# here. That only happens for a project whose teardown noop'd against
# a live daemon — rare, bounded to one process, and cleared by the
# next bootstrap. The durable fix is daemon-side orphan GC (TODO).
purge_e2e_leftovers() {
    local e2e_rundir="$HOME/Library/Application Support/devm-e2e"

    local orphan_vms=()
    while read -r name; do
        [ -z "$name" ] && continue
        orphan_vms+=("$name")
    done < <(tart list 2>/dev/null | awk 'NR>1 && $2 ~ /^e2e-/ {print $2}')
    if [ "${#orphan_vms[@]}" -gt 0 ]; then
        echo "=== e2e: reaping ${#orphan_vms[@]} leftover e2e-* tart VM(s) ===" >&2
        for name in "${orphan_vms[@]}"; do
            tart stop "$name" >/dev/null 2>&1 || true
            tart delete "$name" >/dev/null 2>&1 || true
        done
    fi

    # candidate leftover pids: anything whose argv references the e2e
    # runtime dir path.
    local candidate_pids
    candidate_pids=$(pgrep -f "$e2e_rundir/" 2>/dev/null || true)

    # exclude descendants of the currently-running devm-e2e daemon: they
    # aren't leftovers, they're its own children (mutagen sync monitor,
    # subshells it forked, etc.). This is a POSIX ps-based ancestor
    # walk so it works even when pgrep -P wouldn't help (multi-level
    # descendants).
    local daemon_pid
    daemon_pid=$(pgrep -f "/usr/local/bin/devm-e2e serve" 2>/dev/null | head -1 || true)

    local pids_to_kill=()
    if [ -n "$candidate_pids" ]; then
        local pid
        for pid in $candidate_pids; do
            if [ -n "$daemon_pid" ] && _is_descendant_of "$pid" "$daemon_pid"; then
                continue
            fi
            pids_to_kill+=("$pid")
        done
    fi

    if [ "${#pids_to_kill[@]}" -gt 0 ]; then
        echo "=== e2e: reaping ${#pids_to_kill[@]} leftover e2e process(es) ===" >&2
        kill -TERM "${pids_to_kill[@]}" 2>/dev/null || true
        sleep 1
        kill -KILL "${pids_to_kill[@]}" 2>/dev/null || true
    fi

    rm -rf /tmp/devm-e2e-* /private/tmp/devm-e2e-* 2>/dev/null || true
}

# _is_descendant_of returns 0 if $1's ancestor chain includes $2.
# Walks up via `ps -o ppid=`. Stops at pid 1. Bounded by
# process-tree depth so no unbounded loop risk.
_is_descendant_of() {
    local child="$1" ancestor="$2"
    local cur="$child"
    local depth=0
    while [ "$cur" != "1" ] && [ "$cur" != "0" ] && [ -n "$cur" ] && [ "$depth" -lt 32 ]; do
        if [ "$cur" = "$ancestor" ]; then
            return 0
        fi
        cur=$(ps -o ppid= -p "$cur" 2>/dev/null | tr -d ' ')
        depth=$((depth + 1))
    done
    return 1
}
