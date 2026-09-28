## devm project

This project uses [devm](https://github.com/mdubb86/devm) — a Mac +
Tart-VM dev sandbox with iron-proxy egress enforcement. `devm.yaml`
at the project root drives the VM's shape (services, ports, env,
volumes, allowlist).

- Mac-side commands: `devm start`, `devm shell`, `devm reconcile`,
  `devm approve`, `devm status`, `devm denials`, `devm teardown`.
- Refresh this stanza when devm ships a new Claude recipe:
  `devm recipes get tool/ai/claude`.
