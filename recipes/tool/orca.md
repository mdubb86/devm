---
name: tool/tool/orca
category: tool
display_name: Orca ADE
description: "Use the Orca ADE against a devm VM via SSH. Zero per-project setup once the ~/.ssh/config include line is in place."
keywords: orca ade agentic ssh remote development
since: recipes-vNEXT
---

# Orca ADE (SSH mode)

Devm exposes every running VM through OpenSSH. Orca's SSH connection
mode drives the VM directly — no server-side install, no AppImage, no
Chromium sandbox. Any ssh_config-aware client works the same way
(Cursor Remote, VS Code Remote-SSH, Zed remote, plain `ssh`).

## Prerequisite (one time)

`devm install` prints this line on any install where it's missing;
paste it into `~/.ssh/config`:

    Include "~/Library/Application Support/devm/ssh_config"

That's the entire setup. Devm re-emits the include file every time a
VM starts, stops, or the daemon restarts.

## Connecting

Once your project's VM is up (`devm shell` or `devm start`), point
Orca at the VM:

- **host:** `devm-<vm-name>` — matches `project.vm_name` in your `devm.yaml`.
- **port:** 22
- **username:** devm
- **identityFile:** leave blank; ssh_config's `IdentityFile` inherits.

Orca uses OpenSSH under the hood and honors `Include`, so the
devm-managed block is picked up transparently.

## Troubleshooting

- **`Could not resolve hostname devm-<vm-name>`**: the Include line
  isn't in `~/.ssh/config`. Run `devm install` to see the reminder.
- **`Connection refused`**: the VM is stopped. `devm shell` starts it.
- **`Host key verification failed`**: devm's persistent host key was
  rotated — usually by `devm stop --destroy`. A fresh cold-start
  regenerates the `known_hosts` entry.
- **`Permission denied (publickey)`**: the per-project client key got
  out of sync with `~devm/.ssh/authorized_keys` in the guest.
  `devm reconcile` re-pipes the bundle and heals.
- **First-line debug**: `ssh -v devm-<vm-name>` shows exactly which
  IdentityFile, HostKeyAlias, and IP ssh is using.
