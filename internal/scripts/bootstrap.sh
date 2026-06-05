#!/usr/bin/env bash
# Bootstrap install — runs ONCE at sandbox create time as the first
# install step. Packages installed here are persisted across sandbox
# restarts.
#
# Kit install steps already run as root, so no sudo is needed (and
# sudo may not be on PATH this early in bringup on minimal bases).
#
# Add anything that should be present in every devm sandbox out of
# the box. Keep this list minimal; project-specific installs belong in
# the user's devm.yaml `install:` block.
set -euo pipefail

# ncurses-term: ships terminfo for hundreds of modern terminals
# (ghostty, kitty, alacritty, wezterm, …). Without it the base image
# only knows ~9 entries (xterm, vt100, etc.) and tools that resolve
# the host TERM (vim, less, fzf, htop, etc.) fall back to dumb-mode
# or render glitches whenever the user is on anything modern.
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ncurses-term
