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

# --- Install base packages (Caddy, dnsmasq, ncurses-term, locales) ---
#
# ncurses-term: ships terminfo for hundreds of modern terminals (ghostty,
# kitty, alacritty, wezterm, …). Without it the base image only knows ~9
# entries (xterm, vt100, …) and tools that resolve $TERM (vim, less, fzf,
# htop, …) fall back to dumb-mode.
#
# locales + en_US.UTF-8 generation: the host-forwarded LANG/LC_* env
# (see internal/orchestrator/terminfo.go forwardEnv) needs a matching
# generated locale in the guest. Debian's stock image only generates the
# C locale, so setlocale(LANG=en_US.UTF-8) warns "cannot change locale …"
# on every bash invocation. Generating the locale in the base image
# means every cloned sandbox inherits it — no per-project reprovision.
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq --no-install-recommends \
  caddy \
  dnsmasq \
  nftables \
  ncurses-term \
  locales
sed -i 's/^# *en_US.UTF-8 UTF-8/en_US.UTF-8 UTF-8/' /etc/locale.gen
locale-gen en_US.UTF-8

# --- Drop the unused `debian` user (uid 1001) ---
userdel -r debian 2>/dev/null || true

# --- Install one-shot rename unit + script ---
# Renames admin (uid 1000) to devm on next boot, BEFORE tart-guest-agent
# starts. The Go builder (internal/image.BuildBaseImage) triggers the
# reboot that fires this. After the rename fires and the identity is
# verified, the Go builder removes this machinery before the final
# poweroff — the saved image ships already-renamed.
cat > /usr/local/bin/devm-rename-user <<'SCRIPT'
#!/bin/bash
set -e
if id devm >/dev/null 2>&1; then exit 0; fi
if ! id admin >/dev/null 2>&1; then exit 0; fi
usermod -l devm admin
usermod -d /home/devm -m devm
groupmod -n devm admin
for u in /usr/lib/systemd/system/tart-guest-agent.service /etc/systemd/system/tart-guest-agent.service; do
  [ -f "$u" ] && sed -i 's/^User=admin$/User=devm/' "$u"
done
# CRITICAL: without daemon-reload, systemd keeps its cached (pre-sed)
# view of tart-guest-agent.service and tries to start the agent with the
# old User=admin. Since usermod already renamed admin -> devm, that
# lookup fails with status=217/USER and the agent never comes up —
# leaving `tart exec` hanging from the Mac. Pinned by
# e2e/test_tart_contract_13_reboot_cycle_survives_user_rename.py.
systemctl daemon-reload
for f in /etc/sudoers.d/*; do
  [ -f "$f" ] || continue
  grep -q '\<admin\>' "$f" && sed -i 's/\<admin\>/devm/g' "$f"
done
SCRIPT
chmod +x /usr/local/bin/devm-rename-user

cat > /etc/systemd/system/devm-rename-user.service <<'UNIT'
[Unit]
Description=Rename admin -> devm (devm bootstrap)
DefaultDependencies=no
Before=tart-guest-agent.service
After=local-fs.target
ConditionPathExists=!/var/lib/devm/user-renamed

[Service]
Type=oneshot
ExecStart=/usr/local/bin/devm-rename-user
ExecStartPost=/bin/sh -c "mkdir -p /var/lib/devm && touch /var/lib/devm/user-renamed"
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
UNIT

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

# One daemon-reload after ALL unit files are written, then enable them.
# Enabling a unit systemd doesn't yet know about generally works (enable
# reads [Install] from the file directly), but keeping this order
# explicit prevents future edits from silently landing in the pre-reload
# window and breaking on systems that cache more aggressively.
systemctl daemon-reload
systemctl enable devm-rename-user.service
systemctl enable devm-ready.target

# --- Clean up ---
apt-get clean
rm -rf /var/lib/apt/lists/*
truncate -s 0 /var/log/*.log 2>/dev/null || true

echo "Base provisioning complete."
