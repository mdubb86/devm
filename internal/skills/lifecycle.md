---
name: lifecycle
description: Reference — when to suggest devm shell, reconcile, stop, teardown, validate.
hidden: true
---

# devm subcommand cheatsheet

| Command | Use when |
|---|---|
| `devm shell` | Bring the sandbox up (cold-start if needed) and drop into an interactive shell. Cheap and idempotent on warm sandboxes. |
| `devm reconcile` | Apply config changes to a running sandbox without restarting. Live-bucket changes only (env, ports, allowed_domains, templates). |
| `devm stop` | Stop the running sandbox (preserves state, fast restart). Use when you want the VM gone but the workspace intact. |
| `devm teardown` | Destroy the sandbox (sbx rm). Required after a teardown-bucket change (install, base_image, mounts, masks, identity). Pair with `devm shell` to recreate fresh. |
| `devm validate` | Lint devm.yaml without touching the sandbox. Good after an interactive edit. |
| `devm status` | Print sandbox state + pending config diff against the snapshot. Use when uncertain whether changes have been applied. |

## Bucket map for config changes

| Change | Bucket | Required action |
|---|---|---|
| `env`, `path`, ports, allowed_domains, templates | **Live** | `devm reconcile` (auto-applied on next `devm shell`) |
| Per-service `startup` | **Stop+shell** | Restart the shell (`exit` then `devm shell`) |
| `install`, `base_image`, masks, mounts, identity | **Teardown+shell** | `devm teardown && devm shell` |

When unsure: `devm status` shows the bucket and required action for any pending change.
