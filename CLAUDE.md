# devm — repo-specific rules

Load-bearing conventions for anyone (human or Claude) working in this
repo. If it's a *general* engineering preference, it lives in memory or
user CLAUDE.md; this file is only for rules whose violation causes
real, non-obvious damage in *this* repo.

## Never install a dev build into the live devm daemon

There are **two** independent devm installations on any dev machine:

| identity | binary | state dir | LaunchDaemon | purpose |
|---|---|---|---|---|
| **prod** | `/usr/local/bin/devm` | `~/Library/Application Support/devm/` | `com.devm.service` | Serves the user's real projects (buzztrack, everstone, …) |
| **e2e** | `/usr/local/bin/devm-e2e` | `~/Library/Application Support/devm-e2e/` | `com.devm.e2e.service` | Test playground for the e2e suite |

The two are separated at the identity layer (`internal/identity`) so
they can coexist without stepping on each other. **You must never
overwrite the prod install with a dev build to test something.** Doing
so kills the user's live sessions (Claude in the guest, running
services, mid-session state) and risks a bad build stranding real
work.

### The rule

- **`devm install`** — installs the *prod* daemon from the current
  binary in `bin/devm`. **Do not run this from a dev build to test
  code.** It's for the initial install of a shipped release only.
- **`just e2e-bootstrap`** — builds a fresh `bin/devm-e2e` from
  current source and installs it into the e2e slot only. This is the
  ONLY way to get your code changes running as a daemon for testing.
  Idempotent-forward: re-run it after any daemon-side change.
- **`just e2e <NAME>`** — runs a non-sudo e2e test against the
  bootstrapped e2e daemon.
- **`just e2e-install <NAME>`** — runs a sudo-marker e2e test (tests
  that themselves exercise `devm install`/`uninstall`/`service
  restart` on the e2e slot). Prompts once for sudo; refreshes in the
  background for the run's duration.

### If you find yourself about to write `devm install` in a suggestion

Stop. Substitute `just e2e-bootstrap`. If you *really* believe the
user wants to replace their prod install (e.g. shipping v0.11.2 after
a real release), ask them explicitly and quote back the risks in this
section. The default answer is always "use the e2e slot."

### Workflow split — sudo vs. no-sudo

- **User drives every step that needs sudo.** That's `just e2e-bootstrap`,
  `just e2e-install <NAME>`, `sudo -v`-refresher activity, and any daemon
  reinstall. Ask the user to run these; explain briefly why and hand them
  the exact commands.
- **Claude drives every step that doesn't need sudo, after bootstrap is
  in place.** That's `just e2e <NAME>`, follow-up probes (`tart list`,
  `ps`, log tails), and any code / test iteration.
- **After a daemon-side code change, the e2e binary MUST be re-bootstrapped
  or the test is running the old binary.** Symptom: a bug you just fixed
  still reproduces. Ask the user to `just e2e-bootstrap` again — don't
  chase "why is my fix not working" without checking the install
  timestamp (`ls -l /usr/local/bin/devm-e2e`).

## Never operate directly on a shared checkout

`/Users/michael/workspace/devm` is a working directory shared across
Claude sessions. All feature work belongs in a git worktree
(`.claude/worktrees/<name>`) so branches don't collide. The
`EnterWorktree` tool creates one. Merging happens back in the shared
checkout only at the end.

## Standing preferences (devm-specific)

- **No auto-releases.** `just release` is user-driven, never Claude-driven.
  Even when a fix looks release-worthy, stop at "branch merged; awaiting
  release sign-off."
- **No archaeology comments.** Code documents what IS, not what was. No
  "migrated from X", "used to be Y", "removed Z because ..." in the tree.
- **No migration/compat notes in devm docs.** Single-user tool; every doc
  describes the current shape only.
- **Recipes stay concise.** Tool-specific facts + minimal `devm.yaml`
  block; no explaining devm's own schema in a recipe.
- **Optional schema fields are nullable pointers.** Never `""`/`0` as an
  "unset" sentinel. `*string`, `*int`, `*bool`.
- **`debuglog.Logf` is for HIGH-VOLUME paths only** — per-request,
  per-connection, per-DNS-query, anything that would drown out useful
  signal if always on. Every lifecycle-event log line (VM start/stop,
  iron-proxy respawn, softnet reap, project rebind, config lock/unlock,
  listener startup) belongs on unconditional `log.Printf` with a
  `"category: "` prefix. When in doubt, use `log.Printf` — a silent
  daemon is the failure mode we keep re-discovering the hard way.

## Where the internal specs / plans / gap matrices live

`docs/superpowers/` — gitignored. Feature-in-progress specs, SDD
progress ledgers, ongoing TODO backlog.
