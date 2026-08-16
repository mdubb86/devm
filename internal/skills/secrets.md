---
name: secrets
description: devm secret — store credentials in the on-disk secret store and reference them from devm.yaml. Iron-proxy substitutes the real value at request time so workloads only ever see opaque tokens.
---

# devm secrets reference

Secrets live in the on-disk store at `~/Library/Application Support/devm/secrets/<project>/<name>` (mode-0600 files under a mode-0700 root), not in `devm.yaml`. In the config file you declare a reference; the CLI reads the file at start time and hands an opaque proxy-token to iron-proxy. Workloads inside the VM only ever see the token string. Iron-proxy substitutes the real credential value when forwarding outbound requests.

**Security posture**: files are 0600 under a 0700 root, so no other user account on the Mac can read them. On a FileVault-enabled Mac the store is encrypted at rest. macOS TCC also gates programmatic access under `~/Library/`. Rotation is `devm secret set <name>` overwrite + `devm reconcile` per project that uses it.

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

**`devm secret set <name>`** — Reads the value from stdin if input is piped; otherwise prompts interactively (no echo) at the terminal. Writes the value to `~/Library/Application Support/devm/secrets/<project-id>/<name>` (mode 0600). Rejects empty values.

**`devm secret get <name>`** — Prints the stored value. The output is masked by default (`ab***yz`); pass `--reveal` to print the raw value.

**`devm secret list`** — Lists all secret names stored for the current project (names only, no values).

**`devm secret delete <name>`** — Removes the named secret's file for the current project.

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

Both halves are required, and `devm` rejects a config that has only one:

- A secret in `env:` but under no allow entry has no host scope, so it is omitted from iron-proxy's config entirely and requests would carry the raw token to the upstream.
- A secret under an allow entry but in no `env:` value is never delivered to the guest, so no request ever contains the token for iron-proxy to substitute.

Neither half is redundant. `env:` maps a secret to one or more **variable names**; `network.allow` maps it to **hosts**. One secret can back several variables — `gh` reads `GH_TOKEN`, other tooling reads `GITHUB_TOKEN`, and both can carry the same `github_token`:

```yaml
env:
  GH_TOKEN: !secret github_token
  GITHUB_TOKEN: !secret github_token
network:
  allow:
    - host: api.github.com
      secrets: [github_token]
```

You may bind one secret across multiple hosts by listing it in multiple allow entries; iron-proxy receives the union of those hosts as the injection scope.

---

## The flow

```
devm shell
  │
  ├─ reads devm.yaml → finds !secret refs
  ├─ reads each ref's file from ~/Library/Application Support/devm/secrets/<project>/
  ├─ collects host bindings from network.allow[*].secrets
  │
  └─ hands the resolved secrets to the daemon at start time

VM env:
  ANTHROPIC_API_KEY=__DEVM_SECRET_anthropic_key__   ← workload sees this

Outbound HTTP from VM → iron-proxy:
  Authorization: Bearer __DEVM_SECRET_anthropic_key__
  → iron-proxy substitutes → Authorization: Bearer sk-ant-...
```

Token format: `__DEVM_SECRET_<name>__` (e.g. `__DEVM_SECRET_anthropic_key__`). Deterministic — the same secret name always produces the same token — so iron-proxy restarts don't strand stale tokens in the VM's environment.

## Where the token is substituted

Request **headers** (all of them, including cookies) and **query parameters**. A token that reaches an API as `?api_key=__DEVM_SECRET_foo__` is swapped just like one in `Authorization`, and the substituted value is re-encoded, so a credential containing `&`, `=`, `+` or `/` can't break out of its parameter.

Request **paths and bodies are not** substituted. A token embedded in a URL path segment reaches the upstream unchanged.

Real credential values are never written to disk; they live only in iron-proxy's process memory.

---

## Failure mode: missing secret

If a `!secret` reference in `devm.yaml` has no matching file in the store, `devm shell` fails immediately with:

```
missing secrets: [<name>] (set with `devm secret set <name>`)
```

The error names every missing key. Run `devm secret set <name>` for each one, then retry.

---

## Failure mode: half-declared secret

A secret named on only one side is rejected at config load:

```
secret "anthropic_key" referenced in env but bound to no host — add it to a
network.allow entry's secrets: list, or requests will carry the unsubstituted token
```

```
secret "github_token" bound to a host in network.allow but never referenced by an
env value — add `SOME_VAR: !secret <name>` under env:, or it is never delivered to the guest
```

Without this check the misconfiguration is silent: the sandbox starts, the workload runs, and the upstream returns 401 because it received the literal `__DEVM_SECRET_<name>__` string.

---

## See also

- `devm skills get schema` — `!secret` tag syntax and the `env:` field.
- `devm skills get service` — daemon install and management.
- `devm skills get routing` — iron-proxy egress allowlist and network model.
