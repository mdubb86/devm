## devm guest

You are inside a devm-managed Tart VM. `$IS_SANDBOX=1` confirms it.
`devm`, `tart`, `just`, `brew`, `launchctl` do NOT exist here — they
are Mac binaries. Ask the Mac user to run them.

Read these skills before acting on the environment or asking the Mac
user for help:

- `gdevm recipes asset get tool/ai/claude skills/devm-guest-environment.md`
  — `$IS_SANDBOX`, `$WORKSPACE`, mutagen sync model, iron-proxy
  egress, `.test` hostnames, filesystem layout.
- `gdevm recipes asset get tool/ai/claude skills/gdevm-guest.md` —
  how to use `gdevm propose|passthrough|upgrade|run`.
- `gdevm recipes asset get tool/ai/claude skills/devm-guest-diagnostics.md`
  — allowlist denial shape, guest-side diagnostic commands, what
  only the Mac user can do, which log tails to ask for.

For any `devm.yaml`, `devm.me.yaml`, `devm.sh`, or `devm.me.sh`
change: edit the file at `/home/devm/<name>` (mutagen syncs it to
the Mac), then run `gdevm propose --reason "..."` so the Mac
reviewer can approve. Never modify the checked-in repo copy for
config changes — the file devm reconciles against is the Mac's, not
the repo's.
