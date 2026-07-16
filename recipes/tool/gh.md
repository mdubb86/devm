---
name: tool/tool/gh
category: tool
display_name: GitHub CLI (gh)
description: "Install `gh` from GitHub's apt repo and route API calls through iron-proxy with a fine-grained PAT stored in devm's macOS-keychain secret store. Real token never touches the VM's disk or env."
keywords: gh github cli fine-grained pat secret iron-proxy
since: recipes-vNEXT
---

# GitHub CLI (`gh`)

Wire up `gh` with a fine-grained personal access token scoped to a
single repository, held in the macOS login keychain and injected on
outbound requests to `api.github.com` by iron-proxy. The token itself
never lands on the VM's disk or in the workload's environment — the
workload sees an opaque placeholder that iron-proxy substitutes on
the wire.

## Fine-grained PAT (one-time, per project)

On github.com → Settings → Developer settings → Personal access tokens
→ **Fine-grained tokens** → *Generate new token*:

- **Repository access:** Only select repositories → the single repo
  this sandbox works on.
- **Permissions:** grant only what the workload needs. Read-only:
  `contents: read`. PR-opening workflow: also `pull_requests: write`.
  Skip everything else.
- **Expiration:** whatever fits — 30-90d is reasonable.

The fine-grained PAT physically cannot see other repos and cannot
touch permissions you didn't grant. That's the whole point of scoping
it here instead of using a classic PAT.

## Store in devm's secret backend

    devm secret set GH_TOKEN

Prompts for the value; stores it under `<project>/GH_TOKEN` in the
macOS login keychain. Never touches disk on the guest.

## devm.yaml

```yaml
packages:
  - wget

install:
  - "sudo mkdir -p -m 755 /etc/apt/keyrings"
  - "wget -qO- https://cli.github.com/packages/githubcli-archive-keyring.gpg | sudo tee /etc/apt/keyrings/githubcli-archive-keyring.gpg > /dev/null"
  - "sudo chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg"
  - "echo 'deb [arch=arm64 signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main' | sudo tee /etc/apt/sources.list.d/github-cli.list > /dev/null"
  - "sudo apt-get update && sudo apt-get install -y gh"

secrets:
  - GH_TOKEN

env:
  GH_TOKEN: !secret GH_TOKEN

network:
  allow:
    - cli.github.com          # install-time: apt repo + keyring
    - host: api.github.com
      secrets: [GH_TOKEN]     # runtime: iron-proxy substitutes
    - github.com              # only if you'll clone/push over HTTPS
```

Notes on the shape:

- `arch=arm64` is hardcoded. Tart is Apple Silicon only, so every
  devm VM is arm64; the usual `dpkg --print-architecture` dance from
  GitHub's install docs is dead weight here.
- `gh` reads `GH_TOKEN` from env and sends it as the bearer token on
  every `gh api ...` call. Iron-proxy sees
  `Authorization: Bearer __DEVM_SECRET_GH_TOKEN__` on the wire and
  swaps in the real value from the keychain. The workload's env holds
  only the opaque placeholder.
- Community-distributed `gh` (some Debian bases pre-package it) is
  known-broken for `2.45.x`/`2.46.x` per GitHub's own docs — the apt
  repo path isn't just preferred, it's required.

## Verifying

```
devm shell
$ gh --version                                # gh version X.Y.Z (deb source)
$ gh api /user                                # your PAT's identity
$ gh api /repos/OWNER/REPO/pulls              # the repo you scoped to
```

If you scoped the token to one repo and
`gh api /repos/OWNER/OTHER-REPO` returns 404 — that's fine-grained
scope working as intended.

## Troubleshooting

- **`gh: command not found`**: install step failed. Read the cold-start
  output for the failing `[N/M]` line. Most common cause:
  `cli.github.com` not in `network.allow` → wget can't fetch the
  keyring.
- **`gh api /user` → 401 Bad credentials**: keychain value is wrong or
  expired. `devm secret set GH_TOKEN` to update, then `devm reconcile`
  so iron-proxy picks up the new value.
- **`gh api /user` → 401 in a fresh sandbox, secret is correct**:
  iron-proxy substitution didn't fire. Verify the `api.github.com`
  allow entry has `secrets: [GH_TOKEN]` (not the bare host form).
- **`git clone https://github.com/...` fails**: `github.com` (bare
  host, no `secrets:`) missing from `network.allow`. Note: gh's API
  host is `api.github.com`; clone/push over HTTPS goes to `github.com`.
