---
name: devm-guest-diagnostics
description: Diagnosing failures from inside the devm guest — what a passthrough or allowlist denial looks like, guest-side diagnostic commands, what only the Mac user can do, and which log tails to ask them for.
---

# Diagnostics from the devm guest

## What an egress denial looks like

Iron-proxy on the Mac gates every outbound flow against
`network.allow` in `devm.yaml`. A rejected request surfaces to your
workload as a 502 (HTTPS) or connection refused. The host is
probably not allowlisted; confirm by asking the Mac user for
`devm denials` output (see below).

If you're mid-task and need broader access, request a supervised
passthrough window with `gdevm passthrough --reason "..."` — see the
`gdevm-guest` skill.

## Guest-side diagnostic commands

- `ss -tulpn` — listening sockets.
- `systemctl status <svc>` / `journalctl -u <svc>` — service state
  and unit logs.
- `getent hosts <host>` / `dig <host>` — DNS resolution.
- `curl -v https://<host>` — outbound reachability.
- `sudo systemctl restart <svc>` — restart your own service.

## What only the Mac user can do

- `devm reconcile`, `devm stop`, `devm start`, `devm teardown`,
  `devm status`, `devm shell`, `devm exec` — all Mac-side.
- Restart iron-proxy, rebind the daemon's proxy listeners, change
  the softnet policy.
- Change the egress allowlist at runtime — edit `devm.yaml`, then
  the Mac user runs `devm reconcile`.

## What to ask the Mac user for

Iron-proxy audit logs, daemon logs, and the softnet trace are all
Mac-side. Ask them to run one of these and share the output:

- `devm status` (in the project dir) — VM + iron-proxy + reconcile
  state.
- `devm denials` — hosts iron-proxy has rejected for this project.
- `ps ax | grep iron-proxy` — is this project's iron-proxy running?
- `tail -50 ~/Library/Logs/devm/<project>-proxy.log` — iron-proxy
  activity.
- `tail -50 ~/Library/Logs/com.devm.service.err.log` — daemon
  errors.
- `tail -50 ~/Library/Logs/devm/<project>-vm.log` — softnet
  ingress/egress.
