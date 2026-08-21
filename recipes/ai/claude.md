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
scripts:
  # Install the Claude CLI and expose it on the system PATH.
  install-claude-cli:
    # Symlink FIRST — the installer runs `claude`, which writes ~/.claude.json.
    # With the symlink in place it writes through into the volume (or reads the
    # persisted config); without it, a fresh stub lands at $HOME root and the
    # startup save-back would copy it over the volume config on every
    # re-provision (install: always precedes startup:).
    - ln -sf /home/devm/.claude/.claude.json /home/devm/.claude.json
    - curl -fsSL https://claude.ai/install.sh | bash
    - sudo install -m 755 /home/devm/.local/bin/claude /usr/local/bin/claude

  # Persist ~/.claude.json (login/account/history) via a volume-backed
  # symlink, re-linked every boot. Not CLAUDE_CONFIG_DIR: the VS Code
  # extension and Orca ignore that env var and read/write the default
  # ~/.claude.json regardless, so the default path must stay canonical.
  link-claude-config:
    # Restore-only save-back: fold a real ~/.claude.json to the volume only if
    # it's genuinely onboarded (hasCompletedOnboarding — a fresh install stub
    # has oauthAccount too, so that's no discriminator) or the volume has no
    # config yet; then re-link.
    - if [ -f /home/devm/.claude.json ] && [ ! -L /home/devm/.claude.json ]; then if grep -qE '"hasCompletedOnboarding"[[:space:]]*:[[:space:]]*true' /home/devm/.claude.json 2>/dev/null || [ ! -f /home/devm/.claude/.claude.json ]; then cp -f /home/devm/.claude.json /home/devm/.claude/.claude.json; fi; fi
    - ln -sf /home/devm/.claude/.claude.json /home/devm/.claude.json

install:
  - ">install-claude-cli"

startup:
  # BucketRestartVM: re-links on cold-start AND every restart, since
  # $HOME is fresh after teardown but the symlink target inside the
  # volume persists.
  - ">link-claude-config"

env:
  CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: "1"

volumes:
  # Claude's ~/.claude: settings, transcripts, memory, plugins, tokens
  # (.credentials.json), and — via the link-claude-config symlink —
  # ~/.claude.json (login/account/project trust). Mounted AT the
  # default path so no CLAUDE_CONFIG_DIR override is needed.
  claude: /home/devm/.claude

network:
  allow:
    - api.anthropic.com         # Claude API (core)
    - claude.ai                 # OAuth login redirect + install.sh
    - console.anthropic.com     # OAuth token refresh (claude.ai accounts)
    - platform.claude.com       # OAuth token exchange + revoke
    - downloads.claude.ai       # native installer + plugin downloads
    - mcp-proxy.anthropic.com   # WebFetch / WebSearch (routed via Anthropic)
    - raw.githubusercontent.com # plugin marketplace + /release-notes
```

## Notes

- **Two-file persistence.** Claude Code splits its state across
  `~/.claude/` (settings, transcripts, plugins, `.credentials.json`
  tokens) AND `~/.claude.json` at `$HOME` root (OAuth account,
  onboarding state, per-project trust, project history). The volume
  covers the directory; `link-claude-config` symlinks the sibling file
  into the same volume so both survive teardown. Persisting only
  `~/.claude` logs you out on every teardown — Claude finds tokens but
  no account and treats it as a fresh install.
- **Why not `CLAUDE_CONFIG_DIR`.** It would move both files into the
  volume, but the VS Code extension and Orca ignore that env var and
  read/write the default `~/.claude.json` regardless. Keep the default
  path canonical and use the symlink.
- **Save-back guard.** If a running session ever clobbers the symlink
  into a real file (atomic temp+rename), the guard folds it back into
  the volume before re-linking — but only when it's genuinely onboarded
  (`hasCompletedOnboarding: true`) or the volume is empty. A fresh
  install stub can never overwrite good persisted state. Residual risk:
  a mid-session clobber followed by a `devm teardown` (not a restart)
  before the next boot loses that session's `.claude.json` deltas.
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
  Ephemeral — the installer re-runs on every cold-start (`install:`
  runs once per VM lifetime).
- **`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`** kills Sentry error
  reporting + telemetry. Cleaner than allowlisting `*.sentry.io`.
- **`raw.githubusercontent.com`** is needed for plugin marketplace
  install counts and `/release-notes`. Drop it if you don't use those.
- If you also need Node for other reasons, install Node via the Node
  recipe — Claude Code's native installer doesn't depend on it.

Upstream network docs: <https://code.claude.com/docs/en/network-config.md>
