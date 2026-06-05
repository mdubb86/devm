package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/mtwaage/devm/internal/config"
	"github.com/mtwaage/devm/internal/orchestrator"
	"github.com/mtwaage/devm/internal/sandbox"
	"github.com/spf13/cobra"
)

var teardownYes bool

var teardownCmd = &cobra.Command{
	Use:   "teardown",
	Short: "Destroy the sandbox entirely (sbx rm)",
	Long: `Discovers active sessions inside the sandbox and prompts before
issuing sbx rm. This deletes the VM, its filesystem, and all
installed state. Use --yes (-y) to skip the prompt. The kit
definition under .devm/ is not touched; devm shell will rebuild
the sandbox from scratch.`,
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
		}
		rc, err := orchestrator.RunStop(ctx, deps, cfg.Project.SandboxName, orchestrator.StopDestroy, teardownYes)
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
