---
name: secrets
description: devm secret — store credentials in the macOS keychain and reference them from devm.yaml. Iron-proxy substitutes the real value at request time so workloads only ever see opaque tokens.
---

# devm secrets reference

Secrets live in the macOS login keychain, not in `devm.yaml`. In the config file you declare a reference; the CLI resolves it from the keychain at start time and hands an opaque proxy-token to iron-proxy. Workloads inside the VM only ever see the token string. Iron-proxy substitutes the real credential value when forwarding outbound requests.

---

## Usage

Store a credential once:

```
devm secret set anthropic_key
```

Then reference it in `devm.yaml` and bind it to the host(s) that should receive the real value:

```yaml
env:
  ANTHROPIC_API_KEY: !secret anthropic_key
network:
  allow:
    - host: api.anthropic.com
      secrets: [anthropic_key]
```

At `devm shell`, the CLI reads `anthropic_key` from the keychain and injects the token `__DEVM_SECRET_anthropic_key__` into the VM's environment under `ANTHROPIC_API_KEY`. Iron-proxy substitutes the real value only on requests destined for hosts listed in `network.allow[].secrets` — a secret not bound to any host is never injected. See **Host scoping** below for details.

---

## Subcommands

**`devm secret set <name>`** — Reads the value from stdin if input is piped; otherwise prompts interactively (no echo) at the terminal. Stores the value in the macOS login keychain under `<project-id>/<name>`. Rejects empty values.

**`devm secret get <name>`** — Prints the stored value. The output is masked by default (`ab***yz`); pass `--reveal` to print the raw value.

**`devm secret list`** — Lists all secret names stored for the current project (names only, no values).

**`devm secret delete <name>`** — Removes the named secret from the keychain for the current project.

All subcommands derive the project ID from `devm.yaml` in the working directory.

---

## Host scoping

By default, a `!secret` reference alone does not cause injection. To inject a secret on outbound requests, you must bind it to one or more hosts via the `secrets:` list on a `network.allow` entry:

```yaml
env:
  ANTHROPIC_API_KEY: !secret anthropic_key
network:
  allow:
    - host: api.anthropic.com
      secrets: [anthropic_key]
```

With this config, iron-proxy substitutes the real value only on requests destined for `api.anthropic.com`. Requests to any other host carry the opaque token unchanged.

A secret referenced in `env:` but not named under any allow entry's `secrets:` is omitted from iron-proxy's config entirely — it is **never injected**. (`internal/serviceapi/ironproxy.go`: `if len(s.Hosts) > 0` — only secrets with a non-empty host list are included in the `secrets` transform.)

You may bind one secret across multiple hosts by listing it in multiple allow entries; iron-proxy receives the union of those hosts as the injection scope.

---

## The flow

```
devm shell
  │
  ├─ reads devm.yaml → finds !secret refs
  ├─ calls macOS keychain: Get("<project-id>/<name>") for each ref
  ├─ collects host bindings from network.allow[*].secrets
  │     anthropic_key → ["api.anthropic.com"]
  │
  ├─ calls daemon: StartVM { Secrets: [{Name, Value, Hosts}, ...] }
  │
  └─ daemon spawns iron-proxy:
       ├─ config YAML declares a `secrets` transform (host-scoped):
       │     proxy_value: "__DEVM_SECRET_anthropic_key__"
       │     source: { type: env, var: "DEVM_SECRET_ANTHROPIC_KEY" }
       │     match_headers: []  (all headers)
       │     rules: [{ host: "api.anthropic.com" }]
       └─ real values injected into iron-proxy's process env
            (never written to the on-disk config file)

VM env:
  ANTHROPIC_API_KEY=__DEVM_SECRET_anthropic_key__   ← workload sees this

Outbound HTTP from VM → iron-proxy:
  Authorization: Bearer __DEVM_SECRET_anthropic_key__
  → iron-proxy substitutes → Authorization: Bearer sk-ant-...
```

Token format: `__DEVM_SECRET_<name>__` (e.g. `__DEVM_SECRET_anthropic_key__`). The token is deterministic — the same secret name always produces the same token, so iron-proxy restarts don't strand stale tokens in the VM's environment.

Real values are passed to iron-proxy exclusively through its process environment (one env var per secret, named `DEVM_SECRET_<UPPERCASED_NAME>`). The on-disk config file contains only the token strings and is written mode 0600.

---

## Why the daemon can't read your keychain

The daemon runs as a macOS LaunchDaemon. Even though the plist sets `UserName` to your account, launchd does not give LaunchDaemon processes access to the user's login keychain — that session context is only available to processes launched after you log in interactively.

The CLI (`devm shell`) runs as you with full login keychain access, so all keychain reads happen there before the `StartVM` call. The daemon receives only the resolved proxy-token map; it never touches the keychain.

See `devm skills get service` for the full daemon overview.

---

## Failure mode: missing secret

If a `!secret` reference in `devm.yaml` has no matching entry in the keychain, `devm shell` fails immediately with:

```
missing secrets in keychain: [<name>] (set with `devm secret set <name>`)
```

The error names every missing key. Run `devm secret set <name>` for each one, then retry.

---

## See also

- `devm skills get schema` — `!secret` tag syntax and the `env:` field.
- `devm skills get service` — daemon install and the LaunchDaemon context.
- `devm skills get routing` — iron-proxy egress allowlist and network model.
