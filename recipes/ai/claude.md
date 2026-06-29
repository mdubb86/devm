---
name: tool/ai/claude
category: ai
display_name: Claude Code
description: Install Claude Code CLI; persist OAuth login + conversation history across teardowns.
keywords: claude anthropic claude-code ai
since: recipes-v1.0.0
---

# Claude Code

Uses the official native installer (no Node dependency) and relocates
all `~/.claude` state onto the host-side workspace bind-mount so it
survives `devm teardown`. The allow list is explicit — every domain
Claude Code needs is listed here.

## devm.yaml additions

```yaml
install:
  - curl -fsSL https://claude.ai/install.sh | bash && install -m 755 /root/.local/share/claude/versions/* /usr/local/bin/claude && install -d -o agent -g agent /home/agent/.local/bin && ln -sf /usr/local/bin/claude /home/agent/.local/bin/claude

env:
  CLAUDE_CONFIG_DIR: $WORKSPACE/.devm/.claude
  CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: "1"

network:
  allow:
    - api.anthropic.com         # Claude API (core)
    - claude.ai                 # OAuth login + install.sh
    - platform.claude.com       # Console account auth
    - downloads.claude.ai       # native installer + plugin downloads
    - raw.githubusercontent.com # plugin marketplace + /release-notes
```

## Notes

- **Why the install command is three steps:** the installer drops the
  binary at `/root/.local/share/claude/versions/*` (install runs as
  root), which isn't on the agent user's PATH. First `install -m 755`
  relocates the binary to `/usr/local/bin/claude` (system PATH, works
  for any user). Then `install -d -o agent` + `ln -sf` creates
  `~/.local/bin/claude` as a symlink to it — Claude Code does a
  self-check that expects its binary at that canonical user path, and
  warns/refuses some operations without it.
- **Binary** lands at `/usr/local/bin/claude` (real file) with
  `/home/agent/.local/bin/claude` → it (symlink). Ephemeral — the
  installer re-runs on every cold-start (`install:` runs once per
  VM lifetime).
- **State** is everything Claude stores under `~/.claude`: OAuth at
  `.credentials.json`, conversation transcripts under
  `projects/<repo>/<session>.jsonl`, memory, history, settings.
  `CLAUDE_CONFIG_DIR` relocates all of it to `$WORKSPACE/.devm/.claude`.
- **Why `.devm/.claude`:** `.devm/` is gitignored by devm convention,
  so OAuth tokens stay off git automatically. `.devm/` lives on the
  workspace bind-mount → host-side → survives `devm teardown`. devm
  never prunes anything under `.devm/` outside its own scripts dir.
- **`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`** kills Sentry error
  reporting + telemetry. Cleaner than allowlisting `*.sentry.io`.
- **`raw.githubusercontent.com`** is needed for plugin marketplace
  install counts and `/release-notes`. Drop it if you don't use those.
- If you also need Node for other reasons, install Node via the Node
  recipe — Claude Code's native installer doesn't depend on it.

Upstream network docs: <https://code.claude.com/docs/en/network-config.md>
