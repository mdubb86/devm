---
name: devm
description: Configure and edit devm.yaml — a Mac+VM dev sandbox tool. Use when the user wants to set up devm in a project, add ports / services / env / install steps / mounts, integrate tools (Claude Code, uv, postgres, etc), or understand devm's bucket / supervision semantics. Run `devm skills list` first to discover content.
allowed-tools: Bash(devm:*)
---

This is a discovery stub. Before responding to anything devm-related:

1. Run `devm skills list` to see available workflow + references.
2. Read what you need with `devm skills get <name>`.
3. For tool integrations, also run `devm recipes search <tool>` and
   fetch matching recipes with `devm recipes get <name>`.

The CLI always serves content matching the installed devm version, so
these references can't drift.
