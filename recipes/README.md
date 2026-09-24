# devm recipes

Tool integration snippets the agent fetches when adding a tool to a
project. Each recipe is a single markdown file with YAML frontmatter,
and shows a `devm.yaml` fragment and a `devm.sh` fragment side by
side — a project's config is these two files: `devm.yaml` (schema)
and `devm.sh` (the bash functions `devm.yaml` calls into).

## Layout

```
recipes/
├── <category>/
│   └── <name>.md
```

Categories: `lang`, `db`, `ai`, `service`, `cli`. Add new categories
sparingly.

## Frontmatter (required)

```yaml
---
name: tool/<category>/<name>
category: <category>
description: One short line. Agent grep-able.
keywords: comma or space separated, indexed for search
display_name: Human-friendly title (optional, defaults to name)
since: recipes-v1.0.0 (optional)
---
```

## The two-file model

A recipe touches at most two files:

- **`devm.yaml`** — schema: `packages`, `path`, `env`, `network`,
  `services`, `repos`, `volumes`, etc. Shown in a fenced ` ```yaml `
  block.
- **`devm.sh`** — a plain bash function library. Two names are magic:
  `install()` runs once per VM lifetime, `startup()` runs on every
  boot — both after the workspace is hydrated, still under open
  egress. Everything else is a plain function, callable from
  `install()`/`startup()`, from a `services.<name>.exec:` reference,
  or via `run <name>` when listed in a `repos.<name>.commands:` array.
  Shown in a fenced ` ```bash ` block, right after the yaml block.
  Only recipes with actual shell logic have one.

Adding N recipes to a project gives you N yaml fragments — merge them
into the project's single `devm.yaml` — and however many of those
recipes have a bash fragment, merge those into the project's single
`devm.sh`. Merging is semantic, not text-paste: `env`/`network.allow`/
`packages` entries union together, and if two recipes' bash fragments
both define `install()` (or `startup()`), fold their bodies into one
function — a second `install()` definition in the same file silently
shadows the first, it doesn't run alongside it.

Recipe authors avoid that collision on every OTHER function name by
prefixing with the tool: `install-go-toolchain`, `link-claude-config`,
`preseed-python`. Two recipes' helpers only collide if they picked the
same tool-specific name, which a grep of the target `devm.sh` catches
before applying.

## What devm guarantees for `install()`/`startup()`

You can assume all of the following — no need to rewrite for
robustness:

- **Runs under `bash -o pipefail -c`.** A failing pipeline stage fails
  the whole step. Write `curl ... | bash` directly; you don't need to
  split it into download-then-bash.
- **All persistent env is exported.** Project-wide `env:` plus injected
  `WORKSPACE` and `IS_SANDBOX` are available, both at install time and
  in every later shell session. Use `$WORKSPACE` freely in commands.
- **`apt-get update` already ran.** User entries can `apt-get install
  -y <pkg>` directly. Don't repeat the update.
- **Each phase's stdout+stderr is captured.** A failure surfaces with
  a structured error block (see `devm skills get errors`).

## Style rules

- **Be terse.** Every recipe loaded by the agent costs context tokens.
  Link out to upstream docs rather than inlining them.
- **Show the devm.yaml and devm.sh additions in fenced code blocks,
  side by side.** That's the primary thing the agent will apply.
- **Don't include tutorials.** Recipes are how-to-wire-up, not
  how-to-use.
- **Don't include shell prompts (`$ `)** in the devm.sh block. The
  agent doesn't run the commands — it writes the function.

## Build

The recipes catalog is built by `go run ./tools/build-recipes-db` and
shipped as a SQLite database released independently with `recipes-v*`
tags. See `.github/workflows/recipes-release.yml`.
