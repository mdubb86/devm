package main

import (
	"fmt"
	"os"

	"github.com/mattn/go-isatty"
	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/orchestrator"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/spf13/cobra"
)

var (
	reconcileDryRun bool
	reconcileYes    bool
	reconcileJSON   bool
)

var reconcileCmd = &cobra.Command{
	Use:   "reconcile",
	Short: "Render devm.yaml -> .devm/, diff against running sandbox, apply or prompt",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		repoRoot, err := os.Getwd()
		if err != nil {
			return err
		}
		cfg, err := config.Load(repoRoot)
		if err != nil {
			return err
		}
		tr := tart.New()
		opts := orchestrator.ReconcileOptions{
			DryRun:         reconcileDryRun,
			Yes:            reconcileYes,
			JSON:           reconcileJSON,
			NonInteractive: !isatty.IsTerminal(os.Stdin.Fd()),
		}
		rc, res, err := orchestrator.RunReconcile(cfg, tr, repoRoot, opts)
		if err != nil {
			return err
		}
		if reconcileJSON {
			fmt.Println(orchestrator.FormatReconcileJSON(res))
		} else {
			fmt.Print(orchestrator.FormatReconcileText(res))
			if res.NextAction == "nothing_to_do" && res.SandboxState == "stopped" {
				fmt.Println("Sandbox stopped; config changes will apply on next `devm shell`.")
			}
		}
		if rc != 0 {
			os.Exit(rc)
		}
		return nil
	},
}

func init() {
	reconcileCmd.Flags().BoolVar(&reconcileDryRun, "dry-run", false, "Print diff without applying")
	reconcileCmd.Flags().BoolVarP(&reconcileYes, "yes", "y", false, "Skip the recreate confirmation prompt")
	reconcileCmd.Flags().BoolVar(&reconcileJSON, "json", false, "Emit JSON output")
	rootCmd.AddCommand(reconcileCmd)
}
