package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/mdubb86/devm/internal/release"
	"github.com/mdubb86/devm/internal/serviceapi"
)

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
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
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

		// Drift auto-heal: if the running service is on an older
		// version than this CLI (because the binary on disk has been
		// upgraded since the service was last started), restart the
		// service in-line before the user's command runs. No-op when
		// the service is down or already in sync.
		ensureDaemonInSync(cmd.Context(), cmd.Name())
	},
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// skipDriftHealCommands are the subcommands where auto-heal must NOT
// run: lifecycle commands manage the service themselves (loops) and
// `upgrade` will restart the service when it finishes.
var skipDriftHealCommands = map[string]struct{}{
	"install":        {},
	"uninstall":      {},
	"start":          {},
	"stop-service":   {},
	"restart":        {},
	"service-status": {},
	"serve":          {},
	"upgrade":        {},
}

// ensureDaemonInSync detects drift between the CLI version and the
// running daemon, and silently restarts the daemon when they disagree.
// On dev builds (Version == "dev") we skip — restarting a tagged
// daemon into a dev build would be a downgrade.
func ensureDaemonInSync(ctx context.Context, cmdName string) {
	if Version == "dev" {
		return
	}
	if _, skip := skipDriftHealCommands[cmdName]; skip {
		return
	}
	c := serviceapi.NewClient()
	serviceVer, err := c.Version(ctx)
	if err != nil {
		return // service down; nothing to heal
	}
	if serviceVer == Version {
		return
	}
	if err := restartAndWait("stale daemon, was " + serviceVer); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
}
