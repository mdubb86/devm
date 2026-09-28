---
name: devm-guest-environment
description: The devm guest's view of its own environment — mode detect via $IS_SANDBOX, $WORKSPACE and mutagen sync, iron-proxy egress, .test hostname routing, filesystem layout, reserved env vars. Read this to understand where you are and how the network and filesystem behave from inside the VM.
---

# devm guest environment

You are inside a devm-managed Tart VM: Debian arm64, provisioned by
devm on the Mac.

**Mode detect:** `$IS_SANDBOX == "1"` confirms you are inside the guest.

`devm`, `tart`, `just`, `brew`, `launchctl` do NOT exist here. They
are Mac binaries. Ask the Mac user to run them.

## Reserved env

`env:` in `devm.yaml` is set **everywhere** in the guest — every
login shell, every non-login shell, every child process, every
systemd service. If a variable is declared, it is set; nowhere to
check, nothing to source manually.

Always available:

- `IS_SANDBOX=1` — mode detect.
- `WORKSPACE` — absolute path to the guest workspace at
  `/home/devm/<label>` (kept in sync with a Mac-side mirror via
  mutagen; see Filesystem).
- Everything else from `env:` in `devm.yaml`.

## Filesystem

- `$WORKSPACE` is at `/home/devm/<label>` on the guest, where
  `<label>` is the repo's explicit `label:` or, by default, the leaf
  of the Mac project directory. It is hydrated at first cold-start
  via `git clone` through iron-proxy secret substitution (`repos:`
  in `devm.yaml`), then kept in sync with a devm-owned Mac-side
  mirror by a mutagen two-way sync session — writes on either side
  propagate to the other without a manual push/pull.
- **The mirror is a devm-managed directory, not the Mac project
  directory.** The Mac's project-root `devm.yaml` (what `devm
  reconcile` reads) is separate; edits made in `$WORKSPACE/devm.yaml`
  on the guest do NOT reach that file. Guest-side `devm.yaml` edits
  belong in `/home/devm/devm.yaml` (see the `gdevm-guest` skill for
  the propose flow).
- Other volumes (declared as `volumes:` in `devm.yaml`) work the same
  way: a devm-managed Mac-side mirror directory kept in sync with the
  guest path via mutagen, surviving `devm teardown` (which wipes
  everything else on the VM disk).
- `/opt/devm/` is managed by devm's bundle installer. Don't edit
  files there directly — every provision rewrites it.

## Network

- All outbound flows through iron-proxy on the Mac. The allowlist is
  `network.allow` in the Mac's `devm.yaml`.
- If `curl https://<host>` fails with 502 or connection refused,
  `<host>` is likely not allowlisted. See the `devm-guest-diagnostics`
  skill for how to confirm.
- Your DNS resolver forwards to softnet's DNS on the Mac.
- `.test` hostnames resolve to sibling devm projects, routed via the
  daemon's HTTP proxy on the Mac.
- Reserved IPs: `192.168.127.1` = your view of the Mac (softnet
  host-side); `192.0.2.1` = iron-proxy's virtual IP for allowlisted
  destinations.

## Lifecycle actions you can take here

- `sudo poweroff` — cleanly stops the VM. devm expects this. Never
  suggest `tart stop` on the Mac; it crashes the guest per
  cirruslabs/tart#582, and devm has code to avoid it for this reason.
- `sudo systemctl restart <svc>` — restart your own services.
- Edit `/home/devm/devm.yaml` (or `devm.me.yaml`, `devm.sh`,
  `devm.me.sh`) and use `gdevm propose` to signal it's ready.
