#!/usr/bin/env bash
# purge-leftovers.sh — standalone entry point for the e2e-slot leftover
# sweep. run.sh runs the same sweep automatically before and after every
# test run; `just e2e-bootstrap` runs this before installing so the slot
# starts empty. Also fine to run by hand.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=./sweep.sh
source "$SCRIPT_DIR/sweep.sh"

purge_e2e_leftovers
echo "e2e slot swept"
