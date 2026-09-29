---
name: gdevm-guest
description: gdevm — the guest-side devm dispatcher. Use for propose (signal a devm.yaml edit is ready for review), passthrough (request a supervised egress window), upgrade (refresh the guest bundle), pop (open a file with its default Mac app), and run (invoke a named function from the project's commands manifest).
---

# gdevm — guest-side commands

`gdevm` is the only devm-owned binary installed inside the VM. `devm`
never exists on the guest; `gdevm` never exists on the Mac.

## `gdevm propose`

The `devm.yaml` this VM runs against lives on the Mac, not in your
repo. Mutagen syncs it bidirectionally to `/home/devm/devm.yaml`; edit
that file and the change reaches the Mac automatically. When the edit
is ready for review:

    gdevm propose --reason "add postgres for feature-X"

That records your attribution (branch, cwd, reason) as pending. The
human approves via `devm approve` on the Mac (or the menu-bar app);
`devm reconcile` then applies the change.

`devm.me.yaml`, `devm.sh`, and `devm.me.sh` work the same way — pass
`--kind devm.me.yaml`, `--kind devm.sh`, or `--kind devm.me.sh`.

No commits, no push/pull. These files are not in your repo.

## `gdevm passthrough`

Some tasks need broader outbound access than the project's allowlist
covers — a one-off `curl … | bash`, an `npm install` pulling from an
unlisted registry, an ad-hoc `apt-get` from an unusual mirror. Ask
for a supervised passthrough window instead of hunting for a
workaround:

    gdevm passthrough --reason "need to fetch xyz from unlisted mirror"
    gdevm passthrough --reason "..." --for 15m   # override the default 30s

The daemon records the request as pending. The Mac reviewer runs
`devm passthrough approve` (which honors your requested duration) or
`devm passthrough deny`. Until they act, no window opens.

If the response is `guest.passthrough is disabled`, the project has
opted out (`guest.passthrough: false` in `devm.yaml`); the human is
curating egress directly and will not accept guest-side requests.

## `gdevm upgrade`

Pulls the daemon's current gdevm binary, guest-facing docs,
environment file, and commands manifest into this VM. No args:

    gdevm upgrade

Use it after the Mac side ships a new version and the guest bundle
here is stale (e.g. an unknown subcommand, a missing skill file).

## `gdevm pop`

Opens a guest-side file on the Mac with its default app — a
screenshot, a generated artifact, a log you want to eyeball. Resolves
a `$WORKSPACE`-anchored absolute or relative path, translates it to
the file's Mac-side mirror location, and hands it to macOS `open`:

    gdevm pop ./screenshots/latest.png
    gdevm pop /home/devm/myproj/report.html

An `http://` / `https://` URL routes straight to the default
browser. Extra `open` args pass through after `--`:

    gdevm pop ./notes.md -- -a "Sublime Text"

Paths outside any devm-managed mirror get a live-sync session into a
Mac scratch dir; subsequent guest edits propagate until the session
self-terminates.

## `gdevm run`

Invokes a named function from this project's `commands:` block.
Walks up from `$PWD` to find the containing repo, verifies the name
is registered for that repo, sources `devm.sh` (and `devm.me.sh`),
then re-execs bash from the repo root:

    gdevm run test
    gdevm run db-migrate

Two repos can define the same name; cwd picks the right one.
`commands:` is a list of function names defined in the project's
`devm.sh` or `devm.me.sh`.
