---
name: tool/ai/gsd-core
category: ai
display_name: GSD Core (Get Shit Done)
description: Project-scoped Claude workflow install (roadmaps / phases / plans / executions) under .claude/; gitignored generated surface.
keywords: gsd get-shit-done opengsd claude workflow roadmap plan phase execution planning
since: recipes-v1.0.0
---

# GSD Core

[Get Shit Done](https://www.npmjs.com/package/@opengsd/gsd-core) is a
project-scoped workflow install that drops slash commands, subagents,
hooks, and `settings.local.json` directly into `.claude/`. All of
GSD's generated output should be gitignored.

## devm.yaml additions

```yaml
scripts:
  install-gsd-core:
    # Absolute npx path — install: shell has no login PATH init yet.
    # -y auto-confirms npx's fetch.
    - cd "$WORKSPACE" && /home/devm/.fnm/aliases/default/bin/npx -y @opengsd/gsd-core@latest --claude --local

install:
  # Must sequence AFTER the fnm/Node install (see recipes/lang/node.md).
  - ">install-gsd-core"

network:
  allow:
    - registry.npmjs.org         # npx fetches @opengsd/gsd-core here
    - api.npmjs.org              # metadata + audit
```

## .gitignore additions

GSD's install output is machine-specific (absolute node path) and
regenerated per-install, so nothing under it belongs in git. Untracks
GSD's surface while keeping hand-written `.claude/commands/*.md` and
`.claude/skills/**` tracked:

```gitignore
.claude/agents/gsd-*
.claude/commands/gsd-*
.claude/hooks/
.claude/gsd-core/
.claude/gsd-*.json
.claude/settings.json
.claude/settings.local.json
.claude/package.json
.claude/scripts/
.claude/.gsd-*
.claude/.gsd-staging/
.claude/gsd-migration-journal/
.claude/scheduled_tasks.lock
```

## Notes

- **Not a Claude Code plugin.** Unlike `tool/ai/superpowers` (which
  invokes `claude plugin install ...`), GSD Core is a project-scoped
  file-install: it drops slash commands, subagents, hooks, and
  `settings.local.json` directly under the project's `.claude/`.
- **Rehydration.** `devm teardown` deletes the workspace volume; next
  `devm start` re-hydrates from git (which does not include the GSD
  install), then re-runs `install-gsd-core` in the fresh clone. Nothing
  to remember.
- **Runs in the open-network provisioning window** (install: bucket),
  so no runtime egress opens beyond the two npm hosts.
- **GSD's persistent state lives in `.planning/`** (workspace-tracked,
  bind-mounted, committed). The install is ephemeral; ROADMAPs, PLANs,
  and phase artifacts survive teardown independently.
- **User-scope install** (`--profile default`) would land in `~/.claude/`
  and could ride the volume from `tool/ai/claude`. But that puts Mac
  and VM back on the same `.claude/` — same node-path collision.
  Local + mask + gitignore is the right shape for a shared repo.

## Verifying

Inside `devm shell`, from the project root:

```
$ ls .claude/commands/ | grep -c '^gsd-'      # slash commands (>0)
$ ls .claude/agents/ | grep -c '^gsd-'        # subagents (>0)
$ grep -oE '/[^"]*node[^"]*' .claude/settings.local.json | head -1
                                              # → /home/devm/.fnm/...
```

The last line must be a `/home/devm/.fnm/...` path — never
`/Users/<macuser>/...` or a Homebrew path (that would mean the Mac's
node path leaked in, and every hook would fail with
`/bin/sh: <path>: No such file or directory`).

Upstream: <https://www.npmjs.com/package/@opengsd/gsd-core>
