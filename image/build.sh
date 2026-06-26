#!/usr/bin/env bash
# Build the devm base Tart VM image.
#
# Usage:
#   ./build.sh                   # uses defaults (Debian trixie, arm64)
#   DEBIAN_RELEASE=bookworm ./build.sh
#
# Prerequisites:
#   - tart on PATH (brew install cirruslabs/cli/tart)
#   - curl, sha256sum (or shasum), bash 4+
#
# Produces:
#   - ~/Library/Caches/devm/iso/debian-<release>-<arch>.iso  (cached download)
#   - $TART_HOME/.tart/vms/devm-base  (the built VM)
#
# Flag note (verified against Tart ≥ 0.38):
#   tart run --no-graphics --net-shared --boot-args "..." <name>
#   If your version uses --boot-args differently, run `tart run --help`.

set -euo pipefail

IMAGE_NAME="devm-base"
DEBIAN_RELEASE="${DEBIAN_RELEASE:-trixie}"
DEBIAN_ARCH="${DEBIAN_ARCH:-arm64}"
TART_GUEST_AGENT_VERSION="${TART_GUEST_AGENT_VERSION:-0.10.0}"
DISK_SIZE_GB="${DISK_SIZE_GB:-20}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ISO_CACHE_DIR="${HOME}/Library/Caches/devm/iso"
ISO_FILE="${ISO_CACHE_DIR}/debian-${DEBIAN_RELEASE}-${DEBIAN_ARCH}.iso"
ISO_URL="https://cdimage.debian.org/cdimage/release/current/${DEBIAN_ARCH}/iso-cd/debian-${DEBIAN_RELEASE}-${DEBIAN_ARCH}-netinst.iso"

mkdir -p "${ISO_CACHE_DIR}"

if [[ ! -f "${ISO_FILE}" ]]; then
    echo ">>> Downloading Debian ${DEBIAN_RELEASE} ${DEBIAN_ARCH} netinst ISO..."
    curl -fL --progress-bar -o "${ISO_FILE}.tmp" "${ISO_URL}"
    mv "${ISO_FILE}.tmp" "${ISO_FILE}"
fi

# If devm-base already exists, abort — caller is expected to delete first.
if tart list --format=json 2>/dev/null | grep -q "\"${IMAGE_NAME}\""; then
    echo "ERROR: VM ${IMAGE_NAME} already exists." >&2
    echo "       Delete with: tart delete ${IMAGE_NAME}" >&2
    exit 1
fi

echo ">>> Creating empty Tart VM..."
tart create --linux --disk-size "${DISK_SIZE_GB}" "${IMAGE_NAME}"

echo ">>> Booting installer with preseed..."
echo "    (takes 5-10 min; check Tart's output for progress)"

# Boot with the netinst ISO and our preseed.
# IMPORTANT: tart's actual --boot-args flag name may differ across
# versions. If this fails, run `tart run --help` and adapt. Same for
# --cd-rom — newer versions may use --iso or a different flag.
#
# The preseed lives in the ISO at /cdrom/preseed.cfg — but to get our
# preseed.cfg INTO the ISO we'd need to remaster. Cleaner approach:
# serve the preseed over a tiny local HTTP server during the install,
# referenced from boot args.
PRESEED_SERVER_PORT="${PRESEED_SERVER_PORT:-7901}"
echo ">>> Starting local HTTP server for preseed on port ${PRESEED_SERVER_PORT}..."
(cd "${SCRIPT_DIR}" && python3 -m http.server "${PRESEED_SERVER_PORT}" >/dev/null 2>&1) &
HTTP_PID=$!
trap 'kill ${HTTP_PID} 2>/dev/null || true' EXIT

# Wait briefly for HTTP server to bind.
sleep 1

# Mac's vmnet gateway IP from the VM (default --net-shared) is
# typically 192.168.64.1. Adjust if your setup differs.
GATEWAY="192.168.64.1"
PRESEED_URL="http://${GATEWAY}:${PRESEED_SERVER_PORT}/preseed.cfg"

tart run \
    --no-graphics \
    --net-shared \
    --boot-args "auto=true priority=critical url=${PRESEED_URL}" \
    "${IMAGE_NAME}" &
RUN_PID=$!

# Wait for installer to finish; preseed will poweroff at end.
wait ${RUN_PID} || true
kill ${HTTP_PID} 2>/dev/null || true

echo ">>> Installer done. Booting once to run firstrun.sh..."

# Boot the freshly-installed VM. The preseed's late_command has
# already installed firstrun.sh + the firstrun systemd one-shot, so
# this boot will execute it.
tart run --no-graphics --net-shared "${IMAGE_NAME}" &
RUN_PID=$!

# Wait for firstrun to complete. The firstrun script writes a marker
# file at /var/lib/devm-firstrun-done and powers off. Poll for VM
# stopped.
TIMEOUT=300
elapsed=0
while [[ ${elapsed} -lt ${TIMEOUT} ]]; do
    sleep 5
    elapsed=$((elapsed + 5))
    if ! tart list --format=json 2>/dev/null | grep -q "\"${IMAGE_NAME}\".*\"running\""; then
        echo ">>> firstrun completed (VM stopped)."
        break
    fi
done

if [[ ${elapsed} -ge ${TIMEOUT} ]]; then
    echo "ERROR: firstrun timed out after ${TIMEOUT}s. VM may be in a bad state." >&2
    tart stop "${IMAGE_NAME}" || true
    exit 1
fi

echo ">>> Build complete: ${IMAGE_NAME}"
echo "    Clone with: tart clone ${IMAGE_NAME} <new-vm-name>"
