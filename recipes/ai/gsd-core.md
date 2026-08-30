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
repos:
  main:
    secret: gh_token
    commands:
      install-gsd-core:
        exec: npx @opengsd/gsd-core --claude --local
        startup: true

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
  install), then `install-gsd-core` fires again on boot in the fresh
  clone. Nothing to remember.
- **Runs under open egress** (`commands.*.startup: true` fires
  post-hydration, before the allowlist is applied) — the automatic
  boot-time install has full network access. Both npm hosts stay in
  `network.allow:` anyway, so a manual `run install-gsd-core` from an
  enforced-egress `devm shell` session still works.
- **GSD's persistent state lives in `.planning/`** (workspace-tracked,
  committed). Because it's git-tracked, it survives `devm teardown` on
  its own: the workspace volume is deleted and rehydrated from git on
  the next cold start, and `.planning/` comes back with it. The GSD
  install itself is ephemeral and gets re-run.
- **User-scope install** (`--profile default`) lands in `~/.claude/`,
  riding the volume from `tool/ai/claude` — that's a legitimate choice.
  The local + gitignore shape here is preferred when the project wants
  to ship its GSD conventions (hand-written `.claude/commands/*.md`,
  `.claude/skills/**`) to teammates via git, since those stay
  project-scoped and repo-tracked rather than living in one user's
  home directory.

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
