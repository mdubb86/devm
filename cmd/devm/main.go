package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/mdubb86/devm/internal/release"
	"github.com/mdubb86/devm/internal/serviceapi"
)

// ExitDaemonDrift is the exit code returned when the daemon's
// Fingerprint doesn't match the CLI's. Callers can detect it
// specifically (`if [ $? -eq 3 ]; then devm install; fi`) rather
// than treating any non-zero exit as a drift. 3 is unused by any
// other error path in this binary.
const ExitDaemonDrift = 3

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
// on-disk binary has been rebuilt since the daemon last started.
//
// Runtime cost is zero — the value is a compiled-in string constant
// on both sides. `ensureDaemonInSync` reads it from memory and
// compares against the daemon's `/version` reply.
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

		// Drift detection: if the running daemon was built from a
		// different binary than this CLI (Fingerprint mismatch), the
		// on-disk binary has been rebuilt since the daemon last
		// started. Fail loud so the user (or test infra) knows to
		// run `devm install`. Exits with ExitDaemonDrift (a specific
		// non-1 code) so callers can distinguish drift from generic
		// failure. No-op when the daemon is down or already in sync.
		if err := ensureDaemonInSync(cmd.Context(), cmd.CommandPath()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(ExitDaemonDrift)
		}
	},
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// skipDriftCheckPaths are commands where drift detection must NOT
// run: daemon-lifecycle commands manage the daemon themselves (a
// drift check that raised inside `devm install` would prevent the
// user from ever fixing drift), and `upgrade` restarts the daemon
// on its own. Matched by cobra's full CommandPath — "start" alone
// is ambiguous (`devm start` vs `devm service start`).
var skipDriftCheckPaths = map[string]struct{}{
	"devm install":         {},
	"devm uninstall":       {},
	"devm serve":           {},
	"devm upgrade":         {},
	"devm service start":   {},
	"devm service stop":    {},
	"devm service restart": {},
	"devm service status":  {},
}

// ensureDaemonInSync compares the CLI's Fingerprint against the
// running daemon's Fingerprint. On mismatch, returns an error telling
// the user to run `devm install`; the caller (PersistentPreRun) prints
// it and exits.
//
// Returns nil (no error) when:
//   - the command is in skipDriftCheckCommands (self-managing);
//   - the daemon is down (nothing to compare against — a real command
//     issue will surface if the caller actually needs the daemon);
//   - Fingerprint matches.
//
// Fingerprint is a compiled-in constant, so this is a single unix-
// socket round-trip plus a string equality — cheap enough to run
// on every command that touches the daemon.
func ensureDaemonInSync(ctx context.Context, cmdPath string) error {
	if _, skip := skipDriftCheckPaths[cmdPath]; skip {
		return nil
	}
	c := serviceapi.NewClient()
	daemon, err := c.BuildInfo(ctx)
	if err != nil {
		return nil // service down; nothing to compare
	}
	if daemon.Fingerprint == Fingerprint {
		return nil
	}
	return fmt.Errorf(
		"devm daemon is out of sync with this CLI (daemon fingerprint %s, CLI %s) — run `devm install`",
		daemon.Fingerprint, Fingerprint,
	)
}
