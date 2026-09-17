package main

import (
	"fmt"

	"github.com/mdubb86/devm/internal/config"
	"github.com/spf13/cobra"
)

var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate devm.yaml (and devm.me.yaml if present)",
	Long:  `Validate devm.yaml (and devm.me.yaml if present) against the schema.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		resolved, err := resolveProjectFn()
		if err != nil {
			return err
		}
		cfg, err := config.Load(resolved.StateDir)
		if err != nil {
			return err
		}
		fmt.Printf("OK — %d service(s) configured\n", len(cfg.Services))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(validateCmd)
}
