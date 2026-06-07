#!/usr/bin/env bash
# wrap-fg.sh — wrap a foreground install/startup step.
#
# Usage: wrap-fg.sh <phase> <N> -- <user argv...>
#
# Behavior:
#   1. Create /tmp/.devm-<phase>/<phase>-<N>/ for the per-step log.
#   2. Source $WORKSPACE_DIR/.devm/.env (cfg.Env + WORKSPACE + IS_SANDBOX).
#   3. Redirect stdout+stderr to <dir>/current.
#   4. Capture $? as the user cmd's rc.
#   5. Write <phase>-<N>.rc and <phase>-<N>.ok (if rc==0).
#   6. On failure (rc != 0), MIRROR the failure record to the workspace
#      mount at $WORKSPACE_DIR/.devm/failures/<phase>-<N>.{current,rc}
#      so devm can read it on the host AFTER sbx tears down the sandbox
#      on install: failure (contract_02). Pinned by c32-c34. No chown
#      needed (c33 pinned that the host can read+delete root-in-VM-written
#      files on this stack without sudo).
#   7. Exit with the user's rc.
#
# Reserved arg separator: "--" between wrapper args and user argv.

set -o pipefail
phase=$1; n=$2; shift 2
[ "$1" = "--" ] && shift

base=/tmp/.devm-${phase}
dir=${base}/${phase}-${n}
mkdir -p "$dir"

[ -f "$WORKSPACE_DIR/.devm/.env" ] && . "$WORKSPACE_DIR/.devm/.env"

"$@" > "$dir/current" 2>&1
rc=$?

echo $rc > "${base}/${phase}-${n}.rc"
if [ $rc -eq 0 ]; then
    touch "${base}/${phase}-${n}.ok"
else
    # Mirror failure record to workspace mount. Best-effort —
    # any write failure here must NOT mask the user's rc.
    fdir="$WORKSPACE_DIR/.devm/failures"
    mkdir -p "$fdir" 2>/dev/null
    cp "$dir/current" "$fdir/${phase}-${n}.current" 2>/dev/null
    echo $rc > "$fdir/${phase}-${n}.rc" 2>/dev/null
fi
exit $rc
