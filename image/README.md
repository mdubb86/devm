# devm base image definition

Files in this directory define how to build the Tart VM image that
devm clones per project. The daemon's `image.BuildBaseImage()`
invokes `build.sh` from here.

## Files

- `build.sh` — driver: downloads Debian netinst ISO (cached), creates
  empty Tart VM, runs installer with preseed, boots once for firstrun,
  poweroffs. Result is `devm-base` in Tart's local VM cache.
- `preseed.cfg` — Debian installer answers. Includes `late_command`
  that downloads firstrun.sh + the systemd units from a local HTTP
  server (`build.sh` starts one on port 7901 during the install).
- `firstrun.sh` — runs once at first boot via a oneshot systemd unit.
  Installs tart-guest-agent (.deb from upstream releases), configures
  dnsmasq for `*.test → 127.0.0.1`, enables devm-caddy + devm-dns +
  devm-ready.target, self-deletes, powers off.
- `devm-ready.target` — systemd target uniting DNS + Caddy + network.
  User services rendered by `internal/render/systemd.go` depend on
  this target.
- `devm-dns.service`, `devm-caddy.service` — systemd units for the
  base infrastructure.

## Manual build

```bash
# Prerequisites: brew install cirruslabs/cli/tart
bash build.sh
```

Takes 5-10 minutes on first run (most of the time is Debian's mirror
speed for the netinst download + base package install).

## Iterating on the recipe

If you change any file in this directory, the daemon's
`image.DefinitionHash()` will detect the change. Next `devm install`
or `devm upgrade` rebuilds the image.

Existing project VMs keep their old base flavor (Tart clones are
independent disks). To upgrade a project's base: `devm teardown`
then `devm shell`.

## Sources

- [Debian preseed reference](https://www.debian.org/releases/stable/example-preseed.txt)
- [Tart documentation](https://tart.run/)
- [tart-guest-agent releases](https://github.com/cirruslabs/tart-guest-agent/releases)
