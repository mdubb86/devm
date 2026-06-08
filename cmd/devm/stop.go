package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/orchestrator"
	"github.com/mdubb86/devm/internal/sandbox"
	"github.com/spf13/cobra"
)

var stopYes bool

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the sandbox (preserves VM state)",
	Long: `Discovers active sessions inside the sandbox and prompts to confirm
before issuing sbx stop. Use --yes (-y) to skip the prompt. The
sandbox's filesystem and installed tools persist; only the running
state is discarded. Re-launch with devm shell.`,
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

		deps := orchestrator.StopDeps{
			Runner:   sandbox.DefaultRunner{},
			LockPath: filepath.Join(repoRoot, ".devm", "lock"),
			// In/Out left nil → os.Stdin/os.Stderr.
		}
		rc, err := orchestrator.RunStop(ctx, deps, cfg.Project.SandboxName, orchestrator.StopPreserve, stopYes)
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
