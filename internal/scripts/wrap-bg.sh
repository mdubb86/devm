#!/usr/bin/env bash
# wrap-bg.sh — wrap a background (Background: true) startup step.
#
# Usage: wrap-bg.sh <phase> <N> -- <user argv...>
#
# Behavior:
#   1. Create /tmp/.devm-<phase>/<phase>-<N>/.
#   2. Source $WORKSPACE_DIR/.devm/.env.
#   3. Spawn the user command in a subshell with output piped to
#      s6-log (the static binary embedded by devm at .devm/scripts/s6-log);
#      subshell runs in the background (`&`).
#   4. Touch <phase>-<N>.spawned to mark "wrapper reached this line".
#   5. exit 0 immediately — sbx sees the spawn as successful.

phase=$1; n=$2; shift 2
[ "$1" = "--" ] && shift

base=/tmp/.devm-${phase}
dir=${base}/${phase}-${n}
mkdir -p "$dir"

[ -f "$WORKSPACE_DIR/.devm/.env" ] && . "$WORKSPACE_DIR/.devm/.env"

("$@" 2>&1 | "$WORKSPACE_DIR/.devm/scripts/s6-log" -b n20 s1000000 T "$dir") &
touch "${base}/${phase}-${n}.spawned"
exit 0
