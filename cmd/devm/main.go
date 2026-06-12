package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/mdubb86/devm/internal/release"
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
		if _, ok := nudgeForCommand[cmd.Name()]; !ok {
			return
		}
		release.MaybeNudge(
			cmd.Context(),
			os.Stderr,
			Version,
			fetchLatestForCheck,
			release.DefaultBrewLister(),
		)
	},
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
