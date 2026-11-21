---
name: tool/ai/claude
category: ai
display_name: Claude Code
description: Install Claude Code CLI; persist OAuth login + conversation history across teardowns.
keywords: claude anthropic claude-code ai
since: recipes-v1.0.0
---

# Claude Code

Uses the official native installer (no Node dependency) and persists
Claude Code's full state — OAuth login, tokens, transcripts, memory,
settings, MCP servers, per-project trust — across `devm teardown`. The
allow list is explicit.

## devm.yaml additions

```yaml
# devm.yaml
env:
  CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: "1"

snapshots:
  # Claude's ~/.claude: settings, transcripts, memory, plugins, tokens
  # (.credentials.json), and — via the link-claude-config symlink —
  # ~/.claude.json (login/account/project trust). Captured at teardown
  # and restored at install, so OAuth and history survive a recreate
  # without a re-login round trip.
  claude:
    path: /home/devm/.claude
    on_teardown: true

cron:
  sync-gdevm-docs: "0 * * * *"   # refresh the guest briefing hourly

network:
  allow:
    - api.anthropic.com         # Claude API (core)
    - claude.ai                 # OAuth login redirect + install.sh
    - console.anthropic.com     # OAuth token refresh (claude.ai accounts)
    - platform.claude.com       # OAuth token exchange + revoke
    - downloads.claude.ai       # native installer + plugin downloads
    - mcp-proxy.anthropic.com   # WebFetch / WebSearch (routed via Anthropic)
    - docs.claude.com           # WebFetch — official docs (user guides)
    - code.claude.com           # WebFetch — official docs (Claude Code, SDK)
    - raw.githubusercontent.com/anthropics/*  # plugin marketplace + /release-notes
```
```bash
# devm.sh

# Restore first — on a recreate the snapshot repopulates ~/.claude
# (including .claude.json) before anything else runs. On first boot
# there is no archive yet; restore exits 0 and the installer below
# creates the directory from scratch.
install() {
  gdevm snapshot restore claude
  ln -sf /home/devm/.claude/.claude.json /home/devm/.claude.json
  curl -fsSL https://claude.ai/install.sh | bash
  sudo install -m 755 /home/devm/.local/bin/claude /usr/local/bin/claude
}

# Persist ~/.claude.json (login/account/history) via a volume-backed
# symlink, re-linked every boot. Not CLAUDE_CONFIG_DIR: the VS Code
# extension and Orca ignore that env var and read/write the default
# ~/.claude.json regardless, so the default path must stay canonical.
link-claude-config() {
  # Restore-only save-back: fold a real ~/.claude.json to the volume only if
  # it's genuinely onboarded (hasCompletedOnboarding — a fresh install stub
  # has oauthAccount too, so that's no discriminator) or the volume has no
  # config yet; then re-link.
  if [ -f /home/devm/.claude.json ] && [ ! -L /home/devm/.claude.json ]; then
    if grep -qE '"hasCompletedOnboarding"[[:space:]]*:[[:space:]]*true' /home/devm/.claude.json 2>/dev/null || [ ! -f /home/devm/.claude/.claude.json ]; then
      cp -f /home/devm/.claude.json /home/devm/.claude/.claude.json
    fi
  fi
  ln -sf /home/devm/.claude/.claude.json /home/devm/.claude.json
}

# Runs at boot and hourly (cron:), so a snapshot restore never leaves a
# stale copy; the import line makes every Claude session in the guest
# load it.
sync-gdevm-docs() {
  mkdir -p /home/devm/.claude
  gdevm recipes asset get tool/ai/claude gdevm.md > /home/devm/.claude/gdevm.md.tmp
  mv /home/devm/.claude/gdevm.md.tmp /home/devm/.claude/gdevm.md
  grep -qxF '@~/.claude/gdevm.md' /home/devm/.claude/CLAUDE.md 2>/dev/null \
    || printf '\n@~/.claude/gdevm.md\n' >> /home/devm/.claude/CLAUDE.md
}

startup() {
  # Re-link every boot: $HOME is fresh after teardown but the symlink
  # target inside the volume persists.
  link-claude-config
  sync-gdevm-docs
}
```

## Notes

- **Two-file persistence.** Claude Code splits its state across
  `~/.claude/` (settings, transcripts, plugins, `.credentials.json`
  tokens) AND `~/.claude.json` at `$HOME` root (OAuth account,
  onboarding state, per-project trust, project history). The snapshot
  covers the directory; `link-claude-config` symlinks the sibling file
  into the same directory so both ride the snapshot through teardown.
  Persisting only `~/.claude` logs you out on every teardown — Claude
  finds tokens but no account and treats it as a fresh install.
- **Why not `CLAUDE_CONFIG_DIR`.** It would move both files into the
  snapshot, but the VS Code extension and Orca ignore that env var and
  read/write the default `~/.claude.json` regardless. Keep the default
  path canonical and use the symlink.
- **Save-back guard.** If a running session ever clobbers the symlink
  into a real file (atomic temp+rename), the guard folds it back into
  the snapshotted directory before re-linking — but only when it's
  genuinely onboarded (`hasCompletedOnboarding: true`) or the directory
  has no config yet. A fresh install stub can never overwrite good
  persisted state. Residual risk: a mid-session clobber followed by a
  `devm teardown` (not a restart) before the next boot loses that
  session's `.claude.json` deltas.
- **`console.anthropic.com` matters.** For claude.ai accounts, OAuth
  token refresh hits this host. Without it in the allowlist every
  refresh 403s, Claude blanks the stored tokens
  (`accessToken`/`refreshToken` → empty, `expiresAt` → 0), and the
  user is silently logged out. `devm denials` will show it if in
  doubt.
- **Install steps**: `install.sh` runs as the devm user and lands the
  binary at `/home/devm/.local/bin/claude` (Claude's self-check
  canonical user path — must stay there). Second step copies it to
  `/usr/local/bin/claude` so it's on the system PATH for any user.
  Ephemeral — the installer re-runs on every cold-start (`install()`
  runs once per VM lifetime).
- **`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`** kills Sentry error
  reporting + telemetry. Cleaner than allowlisting `*.sentry.io`.
- **`raw.githubusercontent.com/anthropics/*`** covers the plugin
  marketplace and `/release-notes`. Drop it if you don't use those;
  marketplaces from other owners need their own `/<owner>/*` entry.
- If you also need Node for other reasons, install Node via the Node
  recipe — Claude Code's native installer doesn't depend on it.
- **Guest briefing.** `gdevm.md` covers the guest, config proposals,
  egress, and `gdevm`. Only the import line is added to
  `~/.claude/CLAUDE.md`; the rest of that file is yours.

## Setup

**Mac side** (project root) — a `CLAUDE.local.md` stanza telling Claude
Code it's in a devm project. Not copied automatically, so local edits
are never clobbered; re-run to pick up a newer stanza:

```bash
grep -qxF CLAUDE.local.md .gitignore 2>/dev/null || echo CLAUDE.local.md >> .gitignore
devm recipes asset get tool/ai/claude claude-local-mac.md >> CLAUDE.local.md
```

The guest side needs no setup; `sync-gdevm-docs` above handles it.

Upstream network docs: <https://code.claude.com/docs/en/network-config.md>
