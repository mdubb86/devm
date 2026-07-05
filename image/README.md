# devm base image definition

Source-of-truth content for devm's base Tart VM image (`devm-base`).
Every project VM devm creates is a clone of this image.

The actual build logic lives in `internal/image` (Go). This directory
holds the artifacts that ship into the guest — kept here so a human
can read what the VM does without a Go toolchain, and so they can be
embedded into the devm binary at compile time.

## Files

- `provision-base.sh` — runs inside the freshly-cloned VM. Installs
  Caddy / dnsmasq / nftables, masks the apt auto-updaters, drops the
  unused `debian` user, and installs the `admin → devm` rename
  one-shot systemd unit (fires on the next boot before
  tart-guest-agent starts). `//go:embed`ded by `image/embed.go`;
  `internal/image.BuildBaseImage` streams it via
  `tart exec -i devm-base sudo bash -s`.
- `embed.go` — tiny Go shim exporting `ProvisionBaseScript` via
  `//go:embed`. `//go:embed` can't traverse `..`, so the shim lives
  next to the script rather than in `internal/image`.

## How the build runs

`internal/image.BuildBaseImage` (invoked by `devm install` /
`devm upgrade` when `NeedsBuild` says so):

1. `tart pull ghcr.io/cirruslabs/debian:latest` (override with
   `TEMPLATE=`).
2. Delete any stale `devm-base`.
3. `tart clone` the template into `devm-base`.
4. Boot headless; wait for guest-agent IP.
5. Provision via `tart exec -i … sudo bash -s < provision-base.sh`.
6. Poweroff + fresh boot to fire the rename one-shot.
7. Verify `tart exec devm-base id -un` == `devm`.
8. Cleanup the transient rename unit inside the guest.
9. Final poweroff; devm-base ships already-renamed.

## Iterating on the recipe

Change any embedded artifact and `internal/image.DefinitionHash`
detects it. Next `devm install` or `devm upgrade` rebuilds the image.
Bump `definitionVersion` in `internal/image/builder.go` when the
build procedure itself changes (step order, tart flags) even if the
embedded scripts don't.

Existing project VMs keep their old base flavor (Tart clones are
independent disks). To roll a project onto a new base: `devm
teardown` then `devm shell`.

## Sources

- [Tart documentation](https://tart.run/)
- [tart-guest-agent releases](https://github.com/cirruslabs/tart-guest-agent/releases)
- [cirruslabs Debian image](https://github.com/cirruslabs/macos-image-templates)
