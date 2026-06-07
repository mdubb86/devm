#!/usr/bin/env bash
# wrap-fg.sh — wrap a foreground install/startup step.
#
# Usage: wrap-fg.sh <phase> <N> -- <user argv...>
#
# Behavior:
#   1. Create /tmp/.devm/<phase>-<N>/ for the per-step log.
#   2. Source $WORKSPACE_DIR/.devm/.env (cfg.Env + WORKSPACE + IS_SANDBOX).
#   3. Pipe the user command's stdout+stderr through s6-log -b n20 s1000000 T <dir>.
#      TAI64N timestamps; 20 archived files of ~1 MB each.
#   4. Capture ${PIPESTATUS[0]} (user cmd's rc, NOT s6-log's) into <phase>-<N>.rc.
#   5. Write <phase>-<N>.ok if rc == 0.
#   6. exit with the user's rc.
#
# Devm's RunShell readiness gates poll for /tmp/.devm/<phase>-all-ok
# (written by a sibling sentinel step). On missing sentinel, the
# failure reader walks <phase>-*.rc and <phase>-*.ok to identify
# the failing step and pulls its <dir>/current log.
#
# Reserved arg separator: "--" between wrapper args and user argv.
# Users with a literal "--" in their command should quote it.

phase=$1; n=$2; shift 2
[ "$1" = "--" ] && shift

dir=/tmp/.devm/${phase}-${n}
mkdir -p "$dir"

[ -f "$WORKSPACE_DIR/.devm/.env" ] && . "$WORKSPACE_DIR/.devm/.env"

"$@" 2>&1 | s6-log -b n20 s1000000 T "$dir"
rc=${PIPESTATUS[0]}

echo $rc > /tmp/.devm/${phase}-${n}.rc
[ $rc -eq 0 ] && touch /tmp/.devm/${phase}-${n}.ok
exit $rc
