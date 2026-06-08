package main

import (
	"fmt"
	"os"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/orchestrator"
	"github.com/mdubb86/devm/internal/sandbox"
	"github.com/spf13/cobra"
)

var (
	statusLive bool
	statusJSON bool
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show sandbox state, sessions, and pending config changes",
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
		sb := &sandbox.Sandbox{Name: cfg.Project.SandboxName, Runner: sandbox.DefaultRunner{}}
		var res orchestrator.StatusResult
		if statusLive {
			res, err = orchestrator.RunStatusLive(cfg, sb, repoRoot)
		} else {
			res, err = orchestrator.RunStatus(cfg, sb, repoRoot)
		}
		if err != nil {
			return err
		}
		if statusJSON {
			fmt.Println(orchestrator.FormatStatusJSON(res))
		} else {
			fmt.Print(orchestrator.FormatStatusText(res))
		}
		return nil
	},
}

func init() {
	statusCmd.Flags().BoolVar(&statusLive, "live", false, "Cross-check against live sbx state and report drift (slower; v1 checks port mappings)")
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "Emit JSON output")
	rootCmd.AddCommand(statusCmd)
}
