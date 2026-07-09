package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/mdubb86/devm/internal/serviceapi"
)

// daemonCheckCmd is a hidden probe: exit 0 iff the daemon is running
// AND matches this CLI's Fingerprint. Used by e2e/scripts/run.sh to
// decide whether the test-run's pre-install can be skipped, but also
// available as a general-purpose "is my daemon in sync" one-liner.
//
// Three outcomes:
//   - daemon reachable + Fingerprints match:  drift check in
//     PersistentPreRun returns nil, we reach RunE, ping /health,
//     exit 0.
//   - daemon reachable + Fingerprints differ: PersistentPreRun catches
//     the mismatch, prints the drift message, exits ExitDaemonDrift (3).
//     RunE is never called.
//   - daemon unreachable: drift check returns nil (silent when it
//     can't contact the daemon), we reach RunE, /health times out,
//     exit 1 with a "daemon unreachable" message.
//
// Hidden from `devm --help` so it doesn't clutter the user surface.
var daemonCheckCmd = &cobra.Command{
	Use:    "_daemon-check",
	Hidden: true,
	Short:  "Internal: exit 0 iff daemon is running and matches this CLI's Fingerprint",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Second)
		defer cancel()
		if err := serviceapi.NewClient().Health(ctx); err != nil {
			return fmt.Errorf("daemon unreachable: %w", err)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(daemonCheckCmd)
}
