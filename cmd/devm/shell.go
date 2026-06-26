package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/orchestrator"
	"github.com/mdubb86/devm/internal/serviceapi"
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

		// Auto-install routes in vm mode if the project doesn't have
		// any yet. Best-effort: silent if the daemon is down. We
		// don't overwrite an existing route set — the user may have
		// explicitly chosen `devm route local`, and we respect that
		// across stop/start cycles per the Ship 3 design.
		go func() {
			routes := buildRoutes(cfg, serviceapi.ModeVM)
			if len(routes) == 0 {
				return
			}
			rctx, rcancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer rcancel()
			c := serviceapi.NewClient()
			if !c.Available(rctx) {
				return
			}
			existing, err := c.ListRoutes(rctx)
			if err != nil {
				return
			}
			if _, present := existing[cfg.Project.ID]; present {
				return
			}
			_ = c.ApplyRoutes(rctx, cfg.Project.ID, routes)
		}()

		deps := orchestrator.DefaultShellDeps(repoRoot)
		rc, err := orchestrator.RunShell(ctx, deps, cfg, repoRoot, cfg.Project.SandboxName, cmdName, cmdArgs)
		if err != nil {
			// SIGINT during cold start cancels ctx. Suppress the noisy
			// "context canceled" stack and exit 130 (SIGINT convention).
			if errors.Is(err, context.Canceled) {
				fmt.Fprintln(os.Stderr, "aborted")
				os.Exit(130)
			}
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
