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
tart exec -i "${IMAGE_NAME}" sudo bash -s < "${SCRIPT_DIR}/provision-base.sh"

# --- Fire the rename one-shot via a full poweroff + fresh boot ---
# The one-shot is Before=tart-guest-agent, so it must fire before the
# next agent start. In-place `systemctl reboot` is unreliable: the
# outer `tart run` process's guest-agent handshake often does not
# re-establish across an in-VM reboot. A clean poweroff + fresh
# `tart run` sidesteps that — the second run comes up in ~2s with a
# working guest-agent socket.
#
# Each `tart exec` in the readiness loop is wrapped in `timeout 3`
# so a hung guest-agent handshake surfaces as a failed iteration
# instead of blocking indefinitely. If the loop exhausts, that's a
# real hang, not a slow boot.
wait_for_stopped() {
  for _ in {1..30}; do
    if ! tart list --format=json 2>/dev/null | grep -q "\"${IMAGE_NAME}\".*running"; then return 0; fi
    sleep 1
  done
  return 1
}

echo ">>> Powering off VM to release guest-agent state..."
tart exec "${IMAGE_NAME}" sudo systemctl poweroff || true
kill "${TART_PID}" 2>/dev/null || true
if ! wait_for_stopped; then
  echo "ERROR: VM did not stop within 30s after first poweroff" >&2
  tart list --format=json 2>/dev/null | grep "${IMAGE_NAME}" >&2 || true
  exit 1
fi
wait "${TART_PID}" 2>/dev/null || true

echo ">>> Booting fresh to fire rename one-shot..."
tart run --no-graphics "${IMAGE_NAME}" >/dev/null 2>&1 &
TART_PID=$!
trap 'tart stop "${IMAGE_NAME}" 2>/dev/null || true; kill $TART_PID 2>/dev/null || true' EXIT

for i in {1..30}; do
  if timeout 3 tart exec "${IMAGE_NAME}" true 2>/dev/null; then
    echo ">>> VM reachable after ${i}s"
    break
  fi
  sleep 1
done

IDENTITY=$(timeout 5 tart exec "${IMAGE_NAME}" id -un 2>/dev/null || echo unknown)
if [ "${IDENTITY}" != "devm" ]; then
  echo "ERROR: rename one-shot did not fire — tart exec identity is '${IDENTITY}', expected 'devm'" >&2
  exit 1
fi
echo ">>> Rename verified: tart exec runs as devm."

# --- Remove the transient rename machinery so the saved image is clean ---
echo ">>> Cleaning up rename bootstrap unit..."
timeout 30 tart exec "${IMAGE_NAME}" sudo bash -c '
systemctl disable devm-rename-user.service 2>/dev/null || true
rm -f /etc/systemd/system/devm-rename-user.service
rm -f /etc/systemd/system/multi-user.target.wants/devm-rename-user.service
rm -f /usr/local/bin/devm-rename-user
rm -f /var/lib/devm/user-renamed
rmdir /var/lib/devm 2>/dev/null || true
systemctl daemon-reload
'

# --- Clean shutdown — saves clone state ---
echo ">>> Shutting down VM..."
tart exec "${IMAGE_NAME}" sudo systemctl poweroff || true
if ! wait_for_stopped; then
  echo "ERROR: VM did not stop within 30s after final poweroff" >&2
  exit 1
fi
trap - EXIT

echo ">>> devm-base built (cloned from ${TEMPLATE})."
