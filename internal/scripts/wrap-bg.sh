#!/usr/bin/env bash
# wrap-bg.sh — wrap a background (Background: true) startup step.
#
# Usage: wrap-bg.sh <phase> <N> -- <user argv...>
#
# Behavior:
#   1. Create /tmp/.devm/<phase>-<N>/.
#   2. Source $WORKSPACE_DIR/.devm/.env.
#   3. Spawn the user command in a subshell with output piped to
#      s6-log; subshell runs in the background (`&`).
#   4. Touch <phase>-<N>.spawned to mark "wrapper reached this line".
#   5. exit 0 immediately — sbx sees the spawn as successful.
#
# Background daemon supervision (detecting crashes after spawn) is
# OUT OF SCOPE per the supervision design's Non-Goal #1. The captured
# stdout/stderr in <dir>/current is still useful for human inspection
# after the fact.

phase=$1; n=$2; shift 2
[ "$1" = "--" ] && shift

dir=/tmp/.devm/${phase}-${n}
mkdir -p "$dir"

[ -f "$WORKSPACE_DIR/.devm/.env" ] && . "$WORKSPACE_DIR/.devm/.env"

("$@" 2>&1 | s6-log -b n20 s1000000 T "$dir") &
touch /tmp/.devm/${phase}-${n}.spawned
exit 0
