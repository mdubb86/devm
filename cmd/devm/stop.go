package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/orchestrator"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/spf13/cobra"
)

var stopYes bool

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the VM (preserves disk)",
	Long: `Prompts before stopping the project VM via the devm daemon supervisor.
The VM filesystem and installed tools persist; only the running state is
discarded. Re-launch with devm start. Use --yes (-y) to skip the prompt.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		ident := cfg // capture package identity cfg before it's shadowed below
		resolved, err := discoverProjectFn()
		if err != nil {
			return err
		}
		cfg, err := config.Load(resolved.MacCwd)
		if err != nil {
			return err
		}
		if err := daemonHandshake(cmd.Context(), ident, cfg); err != nil {
			return err
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()

		deps := orchestrator.StopDeps{
			Tart:             tart.New(),
			ServiceAPIClient: serviceapi.NewClient(ident),
			Ident:            ident,
			// In/Out left nil → os.Stdin/os.Stderr.
		}
		rc, err := orchestrator.RunStop(ctx, deps, cfg.Project.Name, orchestrator.StopPreserve, stopYes)
		if err != nil {
			return err
		}
		if rc != 0 {
			os.Exit(rc)
		}
		return nil
	},
}

func init() {
	stopCmd.Flags().BoolVarP(&stopYes, "yes", "y", false, "Skip the confirmation prompt")
	rootCmd.AddCommand(stopCmd)
}
