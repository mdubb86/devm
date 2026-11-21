# devm guest

You are inside a devm-managed Tart VM (Debian arm64) whose shape is
controlled from the Mac. `$DEVM_SIDE=guest` confirms it. `devm`, `tart`,
`just`, `brew`, and `launchctl` are Mac binaries and do not exist here —
ask the Mac user to run them. `gdevm` is your side of devm.

## Changing devm config

`devm.yaml`, `devm.me.yaml`, `devm.sh`, and `devm.me.sh` live on the Mac.
Each is synced both ways to `/home/devm/<file>`. Edit them there — never
the copies inside a repo checkout, which devm does not read. Then:

    gdevm propose --reason "add postgres for feature-x"

The daemon validates `devm.yaml`, finds which files changed since the
last approval, and records your reason. The Mac user approves and runs
`devm reconcile`. Nothing applies until they do. A 403 means the project
set `guest.propose: false`.

## Network

All outbound traffic goes through an allowlist (`network.allow` in
`devm.yaml`) enforced on the Mac. A blocked host shows up as a 502 or
connection refused. Fix it permanently by adding the host via the
propose flow, or ask for a temporary unrestricted window:

    gdevm passthrough 10m --reason "fetch xyz from an unlisted mirror"

The Mac user runs `devm passthrough approve` or `deny`; nothing opens
until then. Other devm projects listed under `peer_services:` are
reachable by their devm hostnames; adding one goes through propose.
`192.168.127.1` is the Mac.

## Filesystem

- Repos and `volumes:` are synced both ways with devm-owned directories
  on the Mac. They survive `devm teardown`; everything else on the VM
  disk is wiped.
- `$WORKSPACE` is the primary repo checkout under `/home/devm/`.
- `env:` from `devm.yaml` is set in every shell and service.
- `/opt/devm/` is rewritten on every provision — don't edit it.

## gdevm commands

- `gdevm run [-d] <name> [args]` — run a function listed under the
  repo's `commands:` (defined in `devm.sh`/`devm.me.sh`). `-d` detaches;
  `gdevm runs`, `gdevm logs [-f] <name>`, `gdevm stop <name>` manage it.
- `gdevm pop [--native] <path>` — open a file on the Mac (browser by
  default, the Mac's default app with `--native`).
- `gdevm snapshot create|restore|list|prune|show` — snapshots declared
  under `snapshots:`.
- `gdevm recipes list | get <name>` — devm's recipe catalog.
- `gdevm upgrade` — refresh `gdevm` and the guest bundle after the Mac
  side updates (e.g. an unknown subcommand).

## When something is wrong

- `sudo systemctl restart <svc>` and `journalctl -u <svc>` are yours.
  `sudo poweroff` stops the VM cleanly.
- Mac-only: `devm start|stop|reconcile|teardown|status`, the allowlist,
  the proxy. Ask the Mac user for `devm status`, `devm denials` (blocked
  hosts), or `tail -50 ~/Library/Logs/devm/<project>-proxy.log`.
