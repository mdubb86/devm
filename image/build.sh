#!/usr/bin/env bash
# Build the devm base Tart VM image by cloning the cirruslabs Debian template.
#
# Usage:
#   ./build.sh
#   TEMPLATE=ghcr.io/cirruslabs/debian:bookworm ./build.sh
#
# Prerequisites:
#   - tart on PATH (brew install cirruslabs/cli/tart)
#   - bash 4+
#
# Produces:
#   - $TART_HOME/.tart/vms/devm-base  (the built VM)
#
# Flag note (verified against Tart ≥ 0.38):
#   tart pull / clone / run --no-graphics / exec / stop / list --format=json / ip

set -euo pipefail

IMAGE_NAME="devm-base"
TEMPLATE="${TEMPLATE:-ghcr.io/cirruslabs/debian:latest}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# --- Pull template (cached after first time) ---
tart pull "${TEMPLATE}"

# --- Abort if our image already exists — caller must delete first ---
if tart list --format=json 2>/dev/null | grep -q "\"${IMAGE_NAME}\""; then
  echo "ERROR: VM ${IMAGE_NAME} already exists. Delete with: tart delete ${IMAGE_NAME}" >&2
  exit 1
fi

# --- Clone template under our name ---
tart clone "${TEMPLATE}" "${IMAGE_NAME}"

# --- Boot headless in background; wait for tart-guest-agent IP ---
tart run --no-graphics "${IMAGE_NAME}" >/dev/null 2>&1 &
TART_PID=$!
trap 'tart stop "${IMAGE_NAME}" 2>/dev/null || true; kill $TART_PID 2>/dev/null || true' EXIT

echo ">>> Waiting for VM boot..."
for i in {1..60}; do
  if tart ip "${IMAGE_NAME}" >/dev/null 2>&1; then break; fi
  sleep 2
done

# --- Run provisioning script inside the VM via tart exec ---
echo ">>> Provisioning base layer..."
tart exec "${IMAGE_NAME}" -- bash -s < "${SCRIPT_DIR}/provision-base.sh"

# --- Clean shutdown — saves clone state ---
echo ">>> Shutting down VM..."
tart exec "${IMAGE_NAME}" -- systemctl poweroff || true
for i in {1..30}; do
  if ! tart list --format=json 2>/dev/null | grep -q "\"${IMAGE_NAME}\".*running"; then break; fi
  sleep 2
done
trap - EXIT

echo ">>> devm-base built (cloned from ${TEMPLATE})."
