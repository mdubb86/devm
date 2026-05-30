#!/usr/bin/env bash
# sweep.sh — read $E2E_REGISTRY and remove any resources still listed.
# Sourced by run.sh; also runnable standalone (define E2E_REGISTRY first).

sweep_registry() {
    [ -z "${E2E_REGISTRY:-}" ] && return 0
    [ -s "$E2E_REGISTRY" ] || return 0
    echo "=== e2e: sweeping leaked resources ==="
    while IFS=$'\t' read -r kind val; do
        [ -z "$kind" ] && continue
        case "$kind" in
            sandbox)
                echo "  sbx rm $val"
                sbx rm "$val" >/dev/null 2>&1 || true
                ;;
            workspace)
                echo "  rm -rf $val"
                rm -rf "$val" >/dev/null 2>&1 || true
                ;;
            policy)
                echo "  sbx policy rm network --resource $val"
                sbx policy rm network --resource "$val" >/dev/null 2>&1 || true
                ;;
            *)
                echo "  (unknown kind: $kind)"
                ;;
        esac
    done < "$E2E_REGISTRY"
}
