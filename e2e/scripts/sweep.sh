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

    if pgrep -f "$e2e_rundir/" >/dev/null 2>&1; then
        echo "=== e2e: reaping $(pgrep -f "$e2e_rundir/" | wc -l | tr -d ' ') leftover e2e process(es) ===" >&2
        pkill -TERM -f "$e2e_rundir/" 2>/dev/null || true
        sleep 1
        pkill -KILL -f "$e2e_rundir/" 2>/dev/null || true
    fi

    rm -rf /tmp/devm-e2e-* /private/tmp/devm-e2e-* 2>/dev/null || true
}
