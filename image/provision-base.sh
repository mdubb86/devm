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

# --- Rename `admin` user → `devm`, drop `debian` ---
# tart exec lands as uid 1000 (admin) by default, per the
# cirruslabs template — pinned by
# e2e/test_tart_contract_04_exec_runs_as_non_root.py. Renaming
# admin keeps that uid + sudo intact under the new name. The
# unused `debian` user (uid 1001) goes away.
userdel -r debian 2>/dev/null || true
usermod -l devm admin
groupmod -n devm admin
usermod -d /home/devm -m devm
if [ -f /etc/sudoers.d/99_cirruslabs.cfg ]; then
  sed -i 's/\b\(admin\|debian\)\b/devm/g' /etc/sudoers.d/99_cirruslabs.cfg
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
