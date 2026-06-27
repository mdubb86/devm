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
  dnsmasq

# --- Rename `debian` user → `devm`, drop `admin` ---
userdel -r admin 2>/dev/null || true
usermod -l devm debian
groupmod -n devm debian
usermod -d /home/devm -m devm
if [ -f /etc/sudoers.d/99_cirruslabs.cfg ]; then
  sed -i 's/\bdebian\b/devm/g' /etc/sudoers.d/99_cirruslabs.cfg
fi

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
