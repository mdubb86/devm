package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/mtwaage/devm/internal/config"
	"github.com/mtwaage/devm/internal/orchestrator"
	"github.com/spf13/cobra"
)

var shellCmd = &cobra.Command{
	Use:   "shell [-- COMMAND...]",
	Short: "Bootstrap the sandbox (if needed) and attach an interactive session",
	Long: `Acquires a project-local lock, brings the sandbox to a running state
if it is stopped, reconciles ports, then attaches an interactive shell.
The sandbox auto-stops when the shell exits.

If the sandbox is already running, devm shell skips bootstrap and
attaches immediately. Port reconcile only runs on cold start.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Past arg parsing — errors from here on are runtime, not usage.
		cmd.SilenceUsage = true
		repoRoot, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get cwd: %w", err)
		}
		cfg, err := config.Load(repoRoot)
		if err != nil {
			return err
		}

		cmdName := "bash"
		var cmdArgs []string
		if len(args) > 0 {
			cmdName = args[0]
			cmdArgs = args[1:]
		}

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()

		deps := orchestrator.DefaultShellDeps(repoRoot)
		rc, err := orchestrator.RunShell(ctx, deps, cfg, repoRoot, cfg.Project.SandboxName, cmdName, cmdArgs)
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
	rootCmd.AddCommand(shellCmd)
}
