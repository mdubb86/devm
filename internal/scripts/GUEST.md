# devm guest

You are inside a devm-managed Tart VM. This document is the guest's view of
the world — the network model from here, the filesystem quirks, what you
can do vs. what you must ask the Mac user to do, and where to look when
something breaks.

**Mode detect**: `$IS_SANDBOX == "1"` confirms you are inside the guest.

## What this is

- Debian arm64, provisioned by devm on the Mac.
- Your project's workspace is at `$WORKSPACE` — identical absolute path on
  host and guest via a virtiofs share.
- `devm`, `tart`, `just`, `brew`, `launchctl` do NOT exist here. They are
  Mac binaries. Ask the Mac user to run them.

## Network

- All outbound flows through iron-proxy on the Mac. The allowlist is
  `network.allow` in the Mac's `devm.yaml`.
- If `curl https://<host>` fails with 502 or connection refused, `<host>`
  is likely not allowlisted. Ask the Mac user to run `devm denials` to see
  recently rejected hosts.
- Your DNS resolver forwards to softnet's DNS on the Mac.
- `.test` hostnames resolve to sibling devm projects, routed via the
  daemon's HTTP proxy on the Mac.
- Reserved IPs: `192.168.127.1` = your view of the Mac (softnet host-side);
  `192.0.2.1` = iron-proxy's virtual IP for allowlisted destinations.

## Filesystem

- `$WORKSPACE` is a virtiofs share. Reads and writes are visible on both
  host and guest immediately.
- Volumes (declared as `volumes:` in devm.yaml) are also virtiofs shares —
  same rules, plus they survive `devm teardown` (which wipes the VM disk).
- **Node's `fs.cpSync({recursive: true})` misbehaves on virtiofs**: it
  produces 0-byte, mode-`0200` destination files regardless of source
  mode. Use `cp -pR` from the shell, or `fs.copyFileSync` + explicit
  `fs.chmodSync` in Node. Every other perm-preserving tool (`cp -p`,
  `install -m`, `chmod`, `tar -xp`, `rsync -a`) works correctly.
- Masks (declared as `masks:` in devm.yaml) live on guest-native ext4 at
  `/var/devm/masks/<project>/<path>` — no virtiofs quirks, but wiped by
  `devm teardown`.
- `/opt/devm/` is managed by devm's bundle installer. Don't edit files
  there directly — every provision rewrites it.

## Lifecycle — what you CAN do

- `sudo poweroff` — cleanly stops the VM. devm expects this. (Never
  suggest `tart stop` on the Mac; it crashes the guest per
  cirruslabs/tart#582, and devm has code to avoid it for this reason.)
- `sudo systemctl restart <svc>` — restart your own services.
- Edit `devm.yaml` in `$WORKSPACE`. The edit doesn't take effect until the
  Mac user runs `devm reconcile`.
- Read your own logs: `journalctl -u <svc>`, `/var/log/…`, etc.

## Lifecycle — what you CANNOT do

- `devm reconcile`, `devm stop`, `devm start`, `devm teardown`,
  `devm status`, `devm shell`, `devm exec` — all Mac-side. Ask the user.
- Restart iron-proxy, rebind the daemon's proxy listeners, change the
  softnet policy — all Mac-side.
- Change the egress allowlist at runtime — edit `devm.yaml`, then Mac
  user runs `devm reconcile`.

## Diagnostics from here

- `ss -tulpn` — listening sockets
- `systemctl status <svc>` / `journalctl -u <svc>` — service state
- `getent hosts <host>` / `dig <host>` — DNS resolution
- `curl -v https://<host>` — outbound reachability

## Diagnostics — what to ask the Mac user

Iron-proxy audit logs, daemon logs, and the softnet trace are all
Mac-side. Ask them to run one of these and share the output:

- `devm status` (in the project dir) — VM + iron-proxy + reconcile state
- `devm denials` — hosts iron-proxy has rejected for this project
- `ps ax | grep iron-proxy` — is this project's iron-proxy running?
- `tail -50 ~/Library/Logs/devm/<project>-proxy.log` — iron-proxy activity
- `tail -50 ~/Library/Logs/com.devm.service.err.log` — daemon errors
- `tail -50 ~/Library/Logs/devm/<project>-vm.log` — softnet ingress/egress

## Reserved env

Guaranteed set on every shell here (install-time and runtime):

- `IS_SANDBOX=1` — mode detect
- `WORKSPACE` — absolute path to the project dir (identical on host)
- Plus everything from `env:` in `devm.yaml`.
