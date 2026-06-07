---
name: tool/ai/claude
category: ai
display_name: Claude Code
description: Install Claude Code CLI; persist conversations across teardowns.
keywords: claude anthropic claude-code ai
since: recipes-v1.0.0
---

# Claude Code

## devm.yaml additions

```yaml
install:
  - curl -fsSL https://deb.nodesource.com/setup_22.x | bash -
  - apt-get install -y nodejs
  - npm install -g @anthropic-ai/claude-code

env:
  CLAUDE_CONFIG_DIR: $WORKSPACE/.claude
```

## Notes

- `CLAUDE_CONFIG_DIR` on the workspace mount → conversations persist
  across `devm teardown && devm shell`.
- Add `.claude/` to `.gitignore`.
- If the project ALSO needs Node for other reasons, the Node lines
  collapse into the Node recipe (only install once).
