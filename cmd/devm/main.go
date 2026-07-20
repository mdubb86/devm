package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/release"
	"github.com/mdubb86/devm/internal/serviceapi"
)

// cfg is this binary's compiled-in daemon identity (prod vs. e2e),
// loaded once at package init via the -X identity.Profile ldflag. It's
// the CLI-side counterpart to the daemon's own identity.Load() call in
// cmd/devm/service.go's RunService invocation — both must resolve to
// the same profile for a CLI/daemon pair to agree on socket paths,
// TLD, pool range, etc.
//
// Some command handlers also load a per-project schema.Config from
// devm.yaml and bind it to a local `cfg` — that local shadows this
// package var for the rest of the enclosing scope. Those handlers
// capture this package cfg into a local `ident` before loading the
// project config, so both remain reachable under distinct names.
var cfg = identity.Load()

// ExitDaemonDrift is the exit code `devm status` uses when the daemon
// Fingerprint doesn't match the CLI's. Scripts can distinguish drift
// from generic failure (`if [ $? -eq 3 ]; then devm install; fi`).
// Only `devm status` uses this code — daemon-touching commands that
// fail fast on drift via requireDaemonInSync return a normal error
// and the CLI exits 1.
const ExitDaemonDrift = 3

// ExitReconcileRequired is the exit code `devm status` (project-scoped
// or --all) uses when a shown project's iron-proxy is MISSING or
// STALE — an actionable signal that `devm reconcile` will fix.
// Distinct from ExitDaemonDrift: this is per-project sandbox drift,
// not a daemon-binary mismatch. Action commands (shell/stop/teardown)
// don't use this code — they warn and proceed (exit 0); `devm status`
// is the read-only probe that keys scripts off the exit code.
const ExitReconcileRequired = 4

// nudgeForCommand fires the "newer version available" check before
// the named subcommands. Suppressions (DEVM_NO_UPDATE_CHECK, CI,
// brew, dev builds) live inside MaybeNudge. Cache means most calls
// are ~1ms with no network.
var nudgeForCommand = map[string]struct{}{
	"shell":     {},
	"start":     {},
	"exec":      {},
	"reconcile": {},
	"stop":      {},
	"status":    {},
}

// Build-time injected via -ldflags. Default values are used during
// `go run` / development; goreleaser overrides them on release builds.
//
// Fingerprint is a random per-build stamp — set via
// `-ldflags "-X main.Fingerprint=<random>"` at every `go build`
// invocation. It's how the CLI and the daemon prove they were
// compiled from the same binary: two processes that share the same
// Fingerprint were built together; different Fingerprints mean the
// on-disk binary has been rebuilt since the daemon last started, so
// the API contract between them is not guaranteed.
var (
	Version     = "dev"
	Commit      = "none"
	Date        = "unknown"
	Fingerprint = "dev"
)

var rootCmd = &cobra.Command{
	Use:   "devm",
	Short: "Mac+VM dev sandbox tool",
	// SilenceErrors: we print the error ourselves in main() so cobra's
	// default "Error: ..." prefix doesn't double up.
	//
	// SilenceUsage is intentionally left at its default (false). That
	// way cobra still prints --help on REAL usage errors (bad flag,
	// missing required arg, unknown subcommand) — those fire before
	// RunE runs. Each RunE handler flips SilenceUsage=true on its own
	// command at the top, so once we're past arg validation, runtime
	// errors don't trigger the help dump.
	SilenceErrors: true,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		if _, ok := nudgeForCommand[cmd.Name()]; ok {
			release.MaybeNudge(
				cmd.Context(),
				os.Stderr,
				Version,
				fetchLatestForCheck,
				release.DefaultBrewLister(),
			)
		}
	},
}

func main() {
	if IsSoftnetInvocation(os.Args[0]) {
		runSoftnetAndExit()
	}

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// requireDaemonInSync verifies the running daemon was built from the
// same source as this CLI. Daemon-touching commands (shell, start,
// exec, stop, reconcile, teardown, route local/vm, denials) call this
// at the top of their RunE to fail fast — the CLI/daemon share a Go
// API surface that isn't versioned, so a mismatched pair can produce
// silent corruption or confusing errors deep in a command.
//
// Returns nil (proceed) when:
//   - the daemon is unreachable (a "daemon down" error will surface
//     naturally when the command tries to call it);
//   - either side's Fingerprint is empty (an older CLI or daemon that
//     doesn't carry the field — best we can do is trust);
//   - Fingerprints match.
//
// On mismatch, returns an actionable error naming both binaries and
// pointing at the recovery command. Commands that don't touch the
// daemon (recipes, secret, skills, version) don't call this — they
// work regardless of daemon state.
func requireDaemonInSync(ctx context.Context) error {
	c := serviceapi.NewClient(cfg)
	daemon, err := c.BuildInfo(ctx)
	if err != nil {
		return nil
	}
	if daemon.Fingerprint == "" || Fingerprint == "" || daemon.Fingerprint == Fingerprint {
		return nil
	}
	return fmt.Errorf(
		"devm daemon is out of sync with this CLI — API compatibility not guaranteed.\n"+
			"  daemon: %s (fingerprint %s)\n"+
			"  CLI:    %s (fingerprint %s)\n"+
			"Recovery: `devm install`",
		daemon.BinaryPath, daemon.Fingerprint, resolvedSelfPath(), Fingerprint,
	)
}
