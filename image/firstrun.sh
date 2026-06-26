#!/usr/bin/env bash
# Runs once at first boot after Debian installation. Bakes the rest of
# the devm base image:
#   - tart-guest-agent (so `tart exec` works)
#   - configures dnsmasq for *.test → 127.0.0.1
#   - placeholder Caddy config (real config arrives per-project)
#   - enables devm-ready.target
# Triggers VM poweroff at the end so build.sh's poll detects completion.

set -euo pipefail

LOG=/var/log/devm-firstrun.log
exec > >(tee -a "${LOG}") 2>&1

TART_GUEST_AGENT_VERSION="${TART_GUEST_AGENT_VERSION:-0.10.0}"
ARCH="$(dpkg --print-architecture)"

echo "[firstrun] starting at $(date)"

# 1. tart-guest-agent from Cirrus Labs releases.
TGA_URL="https://github.com/cirruslabs/tart-guest-agent/releases/download/v${TART_GUEST_AGENT_VERSION}/tart-guest-agent_${TART_GUEST_AGENT_VERSION}_linux_${ARCH}.deb"
echo "[firstrun] installing tart-guest-agent ${TART_GUEST_AGENT_VERSION}..."
curl -fL --retry 3 -o /tmp/tart-guest-agent.deb "${TGA_URL}"
apt install -y /tmp/tart-guest-agent.deb
rm -f /tmp/tart-guest-agent.deb
systemctl enable tart-guest-agent

# 2. dnsmasq config: wildcard *.test → 127.0.0.1
echo "[firstrun] configuring dnsmasq..."
cat > /etc/dnsmasq.d/devm-test.conf <<'EOF'
address=/test/127.0.0.1
EOF
systemctl enable dnsmasq

# 3. Caddy: placeholder. Per-project Caddyfile arrives during provision.
echo "[firstrun] placeholder Caddy config..."
mkdir -p /etc/caddy
cat > /etc/caddy/Caddyfile <<'EOF'
# Placeholder. devm provisioner overwrites this per project.
EOF
# The apt-installed Caddy comes with its own systemd unit at
# /lib/systemd/system/caddy.service. We use devm-caddy.service (the
# unit we dropped in via late_command) as the one wired into the
# devm-ready.target.
systemctl enable devm-caddy
systemctl enable devm-ready.target

# 4. systemctl daemon-reload to pick up our drop-ins.
systemctl daemon-reload

# 5. Self-cleanup: remove the pending marker, the firstrun unit,
# and the firstrun script itself.
echo "[firstrun] self-cleanup..."
rm -f /etc/devm-firstrun-pending
systemctl disable devm-firstrun.service || true
rm -f /etc/systemd/system/devm-firstrun.service
systemctl daemon-reload

echo "[firstrun] complete at $(date)"

# 6. Power off so build.sh's poll detects completion.
sleep 5  # let logs flush
systemctl poweroff
