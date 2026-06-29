package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/orchestrator"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/spf13/cobra"
)

var teardownYes bool

var teardownCmd = &cobra.Command{
	Use:   "teardown",
	Short: "Destroy the VM entirely (deletes disk)",
	Long: `Prompts before stopping the project VM and deleting its disk image.
All installed state is lost. Use --yes (-y) to skip the prompt. The kit
definition under .devm/ is not touched; devm shell will rebuild from scratch.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		repoRoot, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get cwd: %w", err)
		}
		cfg, err := config.Load(repoRoot)
		if err != nil {
			return err
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()

		// Remove this project's routes from the daemon. Best-effort:
		// silent if the daemon is down. The "I'm done with this
		// project" signal per the Ship 3 design.
		rctx, rcancel := context.WithTimeout(context.Background(), 2*time.Second)
		c := serviceapi.NewClient()
		if c.Available(rctx) {
			_ = c.RemoveRoutes(rctx, cfg.Project.ID)
		}
		rcancel()

		deps := orchestrator.StopDeps{
			Tart:             tart.New(),
			ServiceAPIClient: c,
			LockPath:         filepath.Join(repoRoot, ".devm", "lock"),
		}
		rc, err := orchestrator.RunStop(ctx, deps, cfg.Project.ID, cfg.Project.VMName, orchestrator.StopDestroy, teardownYes)
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
	teardownCmd.Flags().BoolVarP(&teardownYes, "yes", "y", false, "Skip the confirmation prompt")
	rootCmd.AddCommand(teardownCmd)
}
