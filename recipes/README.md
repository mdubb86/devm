# devm recipes

Tool integration snippets the agent fetches when adding a tool to
devm.yaml. Each recipe is a single markdown file with YAML frontmatter.

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

## What devm guarantees for every `install:` entry

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
- **Each step's stdout+stderr is captured per-step.** A failure surfaces
  with a structured error block (see `devm skills get errors`).

## Style rules

- **Be terse.** Every recipe loaded by the agent costs context tokens.
  Link out to upstream docs rather than inlining them.
- **Show the devm.yaml additions in fenced code blocks.** That's the
  primary thing the agent will apply.
- **Don't include tutorials.** Recipes are how-to-wire-up, not
  how-to-use.
- **Don't include shell prompts (`$ `).** The agent doesn't run the
  commands — it writes the YAML.

## Build

The recipes catalog is built by `go run ./tools/build-recipes-db` and
shipped as a SQLite database released independently with `recipes-v*`
tags. See `.github/workflows/recipes-release.yml`.
