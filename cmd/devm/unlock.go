package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/serviceapi"
)

var unlockCmd = &cobra.Command{
	Use:   "unlock",
	Short: "Make devm.yaml editable while the VM runs (temporarily lifts the config lock)",
	Long: `devm holds devm.yaml (+ devm.me.yaml) host-immutable while the project's
VM is running, so a root guest can never tamper with its own trust
boundary. This lifts that lock for manual edits.

The lock comes back automatically after ` + "`--for`" + ` (default 5m), or sooner
if you run ` + "`devm reconcile`" + ` or ` + "`devm lock`" + `.`,
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
		if err := daemonHandshake(cmd.Context(), cfg); err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
		defer cancel()

		forDur, err := cmd.Flags().GetDuration("for")
		if err != nil {
			return err
		}
		relockSeconds := 0
		if forDur > 0 {
			relockSeconds = int(forDur.Round(time.Second) / time.Second)
		}

		wasLocked, armedRelockSeconds, err := serviceapi.NewClient().UnlockConfig(ctx, cfg.Project.Name, relockSeconds)
		if err != nil {
			return fmt.Errorf("unlock config: %w", err)
		}
		if !wasLocked {
			fmt.Println("devm.yaml was not locked (VM not running, or config_lock is disabled for this project)")
			return nil
		}
		fmt.Printf("devm.yaml editable; auto re-locks in %s (or run `devm reconcile`/`devm lock`)\n",
			(time.Duration(armedRelockSeconds) * time.Second).String())
		return nil
	},
}

var lockCmd = &cobra.Command{
	Use:   "lock",
	Short: "Re-lock devm.yaml immediately",
	Long: `Re-applies devm's host-immutable lock on devm.yaml (+ devm.me.yaml) right
away, ending a temporary ` + "`devm unlock`" + ` early instead of waiting for it to
re-lock on its own.`,
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
		if err := daemonHandshake(cmd.Context(), cfg); err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
		defer cancel()

		if err := serviceapi.NewClient().LockConfig(ctx, cfg.Project.Name); err != nil {
			return fmt.Errorf("lock config: %w", err)
		}
		fmt.Println("devm.yaml re-locked")
		return nil
	},
}

func init() {
	unlockCmd.Flags().Duration("for", 0, "How long to keep devm.yaml unlocked before it re-locks automatically (0 = daemon default, 5m)")
	rootCmd.AddCommand(unlockCmd, lockCmd)
}
