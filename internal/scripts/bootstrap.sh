#!/usr/bin/env bash
# Bootstrap install — runs ONCE at sandbox create time as the first
# install step. Packages installed here are persisted across sandbox
# restarts.
#
# Kit install steps already run as root, so no sudo is needed (and
# sudo may not be on PATH this early in bringup on minimal bases).
#
# apt-get is reachable at install time regardless of the project's
# network.allowed_domains (locked by e2e/test_sbx_04_install_network_
# policy_pin.py). This script fails strict — any failure here means
# something genuinely wrong (apt mirror down, package missing, etc.)
# and the user needs to see it, not a best-effort skip.
set -euo pipefail

# ncurses-term: ships terminfo for hundreds of modern terminals
# (ghostty, kitty, alacritty, wezterm, …). Without it the base image
# only knows ~9 entries (xterm, vt100, etc.) and tools that resolve
# the host TERM (vim, less, fzf, htop, etc.) fall back to dumb-mode
# or render glitches whenever the user is on anything modern.
#
# s6: provides s6-log (used by wrap-fg.sh / wrap-bg.sh to capture
# per-step stdout/stderr into /tmp/.devm/<phase>-<N>/ with rotation).
# Pinned by e2e/test_sbx_contract_25_install_apt_s6_log_works.py.
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ncurses-term s6
