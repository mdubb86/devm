#!/bin/bash
set -euo pipefail

# --- Disable autoupdaters and housekeeping cruft ---
systemctl mask --now \
  unattended-upgrades.service \
  apt-daily.timer apt-daily-upgrade.timer \
  apt-listchanges.timer \
  dpkg-db-backup.timer \
  e2scrub_all.timer \
  man-db.timer

# --- Install base packages (Caddy, dnsmasq) ---
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq --no-install-recommends \
  caddy \
  dnsmasq \
  nftables

# --- Drop the unused `debian` user (uid 1001) ---
# tart exec lands as `admin` (uid 1000), pinned by
# e2e/test_tart_contract_04_exec_runs_as_non_root.py. We leave
# `admin` alone — renaming it would require this script to not
# already be running as admin (chicken-and-egg with
# tart-guest-agent). Future: rename via a systemd one-shot that
# runs before tart-guest-agent on next boot, then reboot the VM
# at the end of build.sh.
userdel -r debian 2>/dev/null || true

# --- Disable cloud-init re-running on subsequent boots ---
touch /etc/cloud/cloud-init.disabled

# --- devm-ready.target unit ---
cat > /etc/systemd/system/devm-ready.target <<'EOF'
[Unit]
Description=devm base provisioning complete
After=network-online.target
Wants=network-online.target

[Install]
WantedBy=multi-user.target
EOF
systemctl enable devm-ready.target

# --- Clean up ---
apt-get clean
rm -rf /var/lib/apt/lists/*
truncate -s 0 /var/log/*.log 2>/dev/null || true

echo "Base provisioning complete."
