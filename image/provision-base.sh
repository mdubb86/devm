#!/bin/bash
set -euo pipefail

# Where the Go builder (internal/image.BuildBaseImage) stages the
# embedded image/ assets (nftables-locked.conf, devm.target) before
# piping this script over stdin. Not derived from $0 — this script
# runs via `bash -s` (stdin), where $0 carries no usable path.
SCRIPT_DIR="/root/devm-image-assets"

# --- Disable autoupdaters and housekeeping cruft ---
systemctl mask --now \
  unattended-upgrades.service \
  apt-daily.timer apt-daily-upgrade.timer \
  apt-listchanges.timer \
  dpkg-db-backup.timer \
  e2scrub_all.timer \
  man-db.timer

# --- Grow the root filesystem to fill the resized virtual disk ---
# The base disk is resized to schema.DefaultDiskSizeGB via
# `tart set --disk-size` before this provisioning boot. cloud-init
# growpart is disabled later in this script, so expand the root
# partition + ext4 FS explicitly now. growpart lives in /usr/bin but
# needs sfdisk from /sbin, which isn't on this script's PATH; set it.
# growpart exits non-zero when the partition is already at max, which
# is fine here — resize2fs is then a safe no-op.
PATH=/usr/sbin:/sbin:$PATH growpart /dev/vda 1 || true
PATH=/usr/sbin:/sbin:$PATH resize2fs /dev/vda1

# --- Install base packages (dnsmasq, ncurses-term, locales) ---
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
  dnsmasq \
  nftables \
  ncurses-term \
  locales \
  libnss3-tools \
  openssh-server
sed -i 's/^# *en_US.UTF-8 UTF-8/en_US.UTF-8 UTF-8/' /etc/locale.gen
locale-gen en_US.UTF-8

# --- Neutralize systemd-resolved and hand DNS to dnsmasq ---
# Cirruslabs' Debian ships systemd-resolved enabled; it binds
# 127.0.0.53:53 and rewrites /etc/resolv.conf as a symlink to its own
# stub, which knows nothing about our *.test drop-in.
#
# We want dnsmasq to own :53 from the moment the guest boots — before
# devm's per-project provisioning runs any install steps that need
# DNS (apt-get, curl, …). Three parts:
#
#   1. Install the dnsmasq drop-in NOW from the staged asset so the
#      config is on disk before dnsmasq first starts. Content-identity
#      with render.DnsmasqConfig is enforced by a Go unit test
#      (internal/render/dnsmasq_test.go).
#   2. Mask systemd-resolved so nothing can start it later.
#   3. Enable dnsmasq at boot AND point /etc/resolv.conf at 127.0.0.1.
#      dnsmasq's drop-in carries `no-resolv` + explicit upstream
#      (softnet gateway 192.168.127.1), so the loopback pointer
#      doesn't create a query loop.
#
# Ordering matters: this MUST run after `apt-get install` above (that
# step needs working DNS via systemd-resolved). The rest of this
# script is local-only — no more network access needed.
install -o root -g root -m 0644 \
    "$SCRIPT_DIR/dnsmasq-devm-test.conf" \
    /etc/dnsmasq.d/devm-test.conf
systemctl mask --now systemd-resolved.service
rm -f /etc/resolv.conf
echo 'nameserver 127.0.0.1' > /etc/resolv.conf
chmod 0644 /etc/resolv.conf
systemctl enable dnsmasq.service

# --- devm sshd hardening ---
# Base image sshd config override. Managed by devm; see
# docs/superpowers/specs/2026-07-14-ssh-access-design.md.
mkdir -p /etc/ssh/sshd_config.d
cat > /etc/ssh/sshd_config.d/devm.conf <<'SSHD_CONF'
# Managed by devm — do not edit.
PasswordAuthentication no
PermitRootLogin no
AllowUsers devm
AcceptEnv TERM COLORTERM LANG LC_ALL LC_CTYPE
SSHD_CONF

# Mask so postinst-triggered start doesn't run against auto-generated
# host keys — the per-project bundle drops devm's own host key and
# unmasks before the provisioner enables + starts ssh.
systemctl mask ssh

# --- devm-managed systemd-timesyncd config (Ship 5) ---
# The guest's NTP client points at interceptedEgressIP (192.0.2.1,
# RFC 5737 documentation range). Under ENFORCED policy, softnet
# intercepts outbound UDP:123 by destination port and forwards to the
# daemon's SNTP responder. Baked into the base image because the config
# is static — no per-project or per-install variation. Previously
# applied in cmd/devm/serviceapi/vm.go's /vm/apply-egress-enforcement
# handler; moved here to avoid systemd's StartLimitBurst rate limit
# under repeated warm attaches (see fix commit).
#
# PollIntervalMaxSec caps the backoff so a Mac wake heals within ~64s.
# Empty FallbackNTP prevents any accidental leak to the public pool
# (the egress firewall would deny it anyway; silencing keeps logs clean).
# See internal/serviceapi/vm.go: const interceptedEgressIP
mkdir -p /etc/systemd/timesyncd.conf.d
cat > /etc/systemd/timesyncd.conf.d/devm.conf <<'DEVM_TIMESYNCD'
[Time]
NTP=192.0.2.1
FallbackNTP=
PollIntervalMinSec=32
PollIntervalMaxSec=64
DEVM_TIMESYNCD
systemctl enable systemd-timesyncd

# --- Boot-integrity gate floor ---
# Lock: replace the stock nftables.conf with the devm locked skeleton and
# enable nftables.service firewall-first (unmasked — it IS the boot lock now).
install -o root -g root -m 0644 "$SCRIPT_DIR/nftables-locked.conf" /etc/nftables.conf
systemctl enable nftables.service

# Gate: install devm.target (NOT enabled — nothing pulls it at boot).
install -o root -g root -m 0644 "$SCRIPT_DIR/devm.target" /etc/systemd/system/devm.target

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

# --- Guest swap (insurance against OOM) ---
# Size = 50% of MemTotal — which reflects tart --memory, so `memory: 8G`
# in devm.yaml yields a 4G swapfile, `memory: 16G` yields 8G, etc.
# vm.swappiness=10 keeps hot pages in RAM; swap is a backstop against
# OOM kills under memory pressure, not a cache-vs-swap balancer.
#
# The setup script recomputes target size every boot and resizes
# /swapfile if it doesn't match — so a `memory:` change in devm.yaml
# propagates on next VM restart without leaving a stale swapfile.
cat > /etc/sysctl.d/60-devm-swap.conf <<'EOF'
vm.swappiness=10
EOF

cat > /usr/local/sbin/devm-swap-setup <<'SCRIPT'
#!/bin/bash
set -eu
mem_kb=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo)
target_mb=$((mem_kb / 2048))  # 50% of RAM, in MB
current_mb=0
[ -f /swapfile ] && current_mb=$(( $(stat -c%s /swapfile) / 1024 / 1024 ))
if [ "$current_mb" -ne "$target_mb" ]; then
  swapoff /swapfile 2>/dev/null || true
  rm -f /swapfile
  fallocate -l "${target_mb}M" /swapfile
  chmod 600 /swapfile
  mkswap /swapfile
fi
swapon --show=NAME --noheadings | grep -Fxq /swapfile || swapon /swapfile
SCRIPT
chmod +x /usr/local/sbin/devm-swap-setup

cat > /etc/systemd/system/devm-swap.service <<'EOF'
[Unit]
Description=devm guest swap (50% of RAM, recomputed on every boot)
DefaultDependencies=no
After=local-fs.target
Before=multi-user.target

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/devm-swap-setup
RemainAfterExit=yes

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
systemctl enable devm-swap.service

# --- Clean up ---
apt-get clean
rm -rf /var/lib/apt/lists/*
truncate -s 0 /var/log/*.log 2>/dev/null || true

echo "Base provisioning complete."
