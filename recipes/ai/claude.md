---
name: tool/ai/claude
category: ai
display_name: Claude Code
description: Install Claude Code CLI; persist OAuth login + conversation history across teardowns.
keywords: claude anthropic claude-code ai
since: recipes-v1.0.0
---

# Claude Code

Uses the official native installer (no Node dependency) and persists all
`~/.claude` state — OAuth login, transcripts, memory, settings — in a
devm volume so it survives `devm teardown`. The allow list is explicit —
every domain Claude Code needs is listed here.

## devm.yaml additions

```yaml
scripts:
  # Install the Claude CLI and expose it on the system PATH.
  install-claude-cli:
    - curl -fsSL https://claude.ai/install.sh | bash
    - sudo install -m 755 /home/devm/.local/bin/claude /usr/local/bin/claude

install:
  - ">install-claude-cli"

env:
  CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: "1"

volumes:
  # Claude's ~/.claude: OAuth (.credentials.json), transcripts, memory,
  # settings. Mounted AT the default path so no CLAUDE_CONFIG_DIR
  # override is needed — Claude runs on its own default location.
  claude: /home/devm/.claude

network:
  allow:
    - api.anthropic.com         # Claude API (core)
    - claude.ai                 # OAuth login + install.sh
    - platform.claude.com       # Console account auth
    - downloads.claude.ai       # native installer + plugin downloads
    - raw.githubusercontent.com # plugin marketplace + /release-notes
```

## Notes

- **Install steps**: `install.sh` runs as the devm user and lands the
  binary at `/home/devm/.local/bin/claude` (Claude's self-check
  canonical user path — must stay there). Second step copies it to
  `/usr/local/bin/claude` so it's on the system PATH for any user.
  Ephemeral — the installer re-runs on every cold-start (`install:`
  runs once per VM lifetime).
- **State** is everything Claude stores under `~/.claude`: OAuth at
  `.credentials.json`, conversation transcripts, memory, settings.
  The `claude` volume mounts at Claude's native default path so no
  `CLAUDE_CONFIG_DIR` override is needed. Survives `devm teardown`.
- **`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`** kills Sentry error
  reporting + telemetry. Cleaner than allowlisting `*.sentry.io`.
- **`raw.githubusercontent.com`** is needed for plugin marketplace
  install counts and `/release-notes`. Drop it if you don't use those.
- If you also need Node for other reasons, install Node via the Node
  recipe — Claude Code's native installer doesn't depend on it.

Upstream network docs: <https://code.claude.com/docs/en/network-config.md>
