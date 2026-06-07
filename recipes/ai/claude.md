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
survives `devm teardown`.

## devm.yaml additions

```yaml
install:
  - curl -fsSL https://claude.ai/install.sh | bash

env:
  CLAUDE_CONFIG_DIR: $WORKSPACE/.devm/.claude
```

## Notes

- **Binary** lands at `~/.local/bin/claude` inside the sandbox. That path
  is ephemeral — the installer re-runs on every cold-start. No state
  there.
- **State** is everything Claude stores under `~/.claude`: the OAuth
  credentials at `.credentials.json`, conversation transcripts under
  `projects/<repo>/<session>.jsonl`, memory, history, settings.
  `CLAUDE_CONFIG_DIR` relocates all of it to `$WORKSPACE/.devm/.claude`.
- **Why `.devm/.claude` (not `.claude`):** `.devm/` is already gitignored
  by devm convention, so OAuth tokens stay off git automatically. `.devm/`
  also lives on the workspace bind-mount, so it's host-side and survives
  sandbox teardown. devm itself never prunes anything under `.devm/`
  outside its own scripts directory.
- **Network:** install fetches from `claude.ai`. If you've narrowed
  `network.allowed_domains`, include it.
- If the project ALSO needs Node for other reasons, install Node
  separately via the Node recipe — Claude Code's native installer
  doesn't need it.
