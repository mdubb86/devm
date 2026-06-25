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

		// Service version-skew warning. Cheap: ~1-2ms when service is
		// running, returns immediately when not. Soft warning only; never
		// blocks the command.
		checkServiceVersionSkew(cmd.Context())
	},
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// checkServiceVersionSkew prints a one-line warning if the running
// devm service reports a different version than this CLI binary.
// Silent when the service isn't running (no install yet, expected).
func checkServiceVersionSkew(ctx context.Context) {
	c := serviceapi.NewClient()
	serviceVer, err := c.Version(ctx)
	if err != nil {
		return // service down; not a warning case
	}
	if serviceVer != Version {
		fmt.Fprintf(os.Stderr,
			"warning: CLI is %s but devm service is %s — run `devm restart` to pick up the new binary\n",
			Version, serviceVer,
		)
	}
}
