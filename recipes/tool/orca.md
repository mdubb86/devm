---
name: tool/tool/orca
category: tool
display_name: Orca ADE
description: "Use the Orca ADE against a devm VM via SSH. Includes the relay's native-build requirements (build-essential + nodejs.org) so first attach doesn't hang on node-gyp."
keywords: orca ade agentic ssh remote development relay node-gyp native-build build-essential nodejs
since: recipes-vNEXT
---

# Orca ADE (SSH mode)

Devm exposes every running VM through OpenSSH. Orca's SSH connection
mode uses OpenSSH for the transport, but on attach Orca **deploys a
small Orca-aware relay** into `~/.orca-remote/` and runs
`npm install --omit=dev node-pty@… @parcel/watcher@…` for native
modules. `node-pty` ships **no linux-arm64 prebuilt**, so it compiles
from source via node-gyp on first attach — the sandbox must be able
to build it.

Plain `ssh`, VS Code Remote-SSH, Cursor, and Zed still connect with
zero server-side install; the relay requirement is Orca-specific.

## Prerequisite (one time)

`devm install` prints this line on any install where it's missing;
paste it into `~/.ssh/config`:

    Include "~/Library/Application Support/devm/ssh_config"

Devm re-emits the include file every time a VM starts, stops, or the
daemon restarts.

## Relay build requirements (per project)

Orca's first-attach `npm install` (native `node-pty` compile) needs
two things a bare devm sandbox lacks. Add them to your project's
`devm.yaml`:

```yaml
packages:
  - build-essential     # node-gyp toolchain; node-pty compiles from
                        # source (no linux-arm64 prebuilt)

network:
  allow:
    - nodejs.org        # node-gyp fetches matching node headers at
                        # build time — a RUNTIME host that surfaces
                        # when Orca attaches, after provisioning
```

## Connecting

Once the VM is up (`devm shell` or `devm start`), point Orca at it:

- **host:** `devm-<name>` — matches `project.name` in your `devm.yaml`.
- **port:** 22
- **username:** devm
- **identityFile:** leave blank; ssh_config's `IdentityFile` inherits.

Orca uses OpenSSH under the hood and honors `Include`, so the
devm-managed block is picked up transparently.

## Opening a project on the VM

The workspace is bind-mounted (virtiofs) at the **mirrored host
path**, not under `/home/devm`. In Orca's "add remote project →
Browse remote filesystem" picker, navigate to:

    /Users/<you>/workspace/<project>

`~/workspace` and any legacy `/w/<project>` path do not exist on the
guest. Orca runs `git worktree add` on the remote under this path.
Note `node_modules` is a separate VM-local volume (devm `masks:`),
so it won't match your Mac's checkout — expected.

## Troubleshooting

- **`Could not resolve hostname devm-<name>`**: the Include line
  isn't in `~/.ssh/config`. Run `devm install` to see the reminder.
- **`Connection refused`**: the VM is stopped. `devm shell` starts it.
- **`Host key verification failed`**: devm's persistent host key was
  rotated — usually by `devm teardown`. A fresh cold-start regenerates
  the `known_hosts` entry.
- **`Permission denied (publickey)`**: the per-project client key got
  out of sync with `~devm/.ssh/authorized_keys` in the guest.
  `devm reconcile` re-pipes the bundle and heals.
- **`Timed out while waiting for handshake` (during attach, not the
  "Test" button)**: the SSH connection succeeded but the relay's
  `npm install` failed. Read the build log on the VM at
  `~/.npm/_logs/*-debug-*.log`. Common causes:
    - `gyp ERR! … 403 … nodejs.org` → `nodejs.org` not in
      `network.allow`. See Relay build requirements.
    - `gyp ERR! … make: not found` / no compiler → `build-essential`
      missing from `packages:`. See Relay build requirements.
- **First-line debug**: `ssh -v devm-<name>` shows exactly which
  IdentityFile, HostKeyAlias, and IP ssh is using.
