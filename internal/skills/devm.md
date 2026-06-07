---
name: devm
description: Init or edit devm.yaml — Mac+VM dev sandbox tool. Use when the user wants to set up devm in a project, add ports/services/env/install/mounts, or integrate tools.
hidden: false
---

# devm — init and edit devm.yaml

You are configuring devm.yaml for the user. Use this skill when they
want to set devm up in a project or change an existing devm.yaml.

## When you load: discover what's available

Run `devm skills list` once at the start of the session to see what
reference content is available. Common reference docs:

- `devm skills get schema` — devm.yaml field reference
- `devm skills get lifecycle` — when to suggest devm shell/reconcile/teardown
- `devm skills get errors` — supervision error patterns

Fetch references on demand only — don't preload them.

## Decide: init or edit?

Check if `devm.yaml` exists in the cwd. If yes → edit flow. If no → init flow.

## Init flow

1. **Scan the project.** Use `ls`, `cat package.json`, `cat pyproject.toml`,
   `cat go.mod`, `cat Cargo.toml`, etc. Identify the stack.
2. **Search recipes for each detected tool.** For each tool you found,
   run `devm recipes search <tool>`. If a recipe matches, fetch it with
   `devm recipes get <name>` and apply its devm.yaml additions.
3. **Propose a minimal devm.yaml.** Include only what the project needs.
   Don't bake in services the user didn't ask for.
4. **Iterate with the user.** Show them the proposal. Ask one focused
   question at a time about anything ambiguous (ports? extra tools?
   mounts?).
5. **Write the file.** Add `.devm/` and `.devm-failures/` to `.gitignore`
   if not already present.
6. **Suggest next step:** `devm shell` to test the bringup.

## Edit flow

1. **Read the existing devm.yaml** carefully.
2. **Understand the user's intent.** Are they adding a service, port,
   env var, install step, mount, network rule?
3. **For tool integrations:** run `devm recipes search <tool>` first.
4. **For schema questions:** run `devm skills get schema`.
5. **Propose the minimal change.** Show a focused diff.
6. **Apply the change.** Validate with `devm validate` if available.
7. **Mention bucket impact:**
   - `env:`, ports → live, picked up on next `devm shell`.
   - `install:` → teardown bucket. Suggest `devm teardown && devm shell`.
   - `services[*].startup:` → stop bucket. Suggest re-shelling.
   (Fetch `devm skills get lifecycle` for the full table.)

## When something goes wrong

If the user runs into a supervision error, fetch `devm skills get errors`
for the diagnosis playbook. Don't try to debug from first principles.

## Do not

- Don't bake in opinionated services (postgres, redis) unless the user
  asked.
- Don't suggest manual `apt-get update` in install: — bootstrap already
  runs it.
- Don't suggest mounting `~/.claude` for Claude Code persistence — use
  `env: CLAUDE_CONFIG_DIR: $WORKSPACE/.claude` instead (see
  `devm recipes get tool/ai/claude`).
