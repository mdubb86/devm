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
