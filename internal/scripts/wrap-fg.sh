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
#   6. Exit with the user's rc.
#
# Devm's RunShell readiness gates poll for /tmp/.devm-<phase>/<phase>-all-ok
# (written by a sibling sentinel step). On missing sentinel, the
# failure reader walks <phase>-*.rc and <phase>-*.ok to identify
# the failing step and pulls its <dir>/current log.
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
[ $rc -eq 0 ] && touch "${base}/${phase}-${n}.ok"
exit $rc
