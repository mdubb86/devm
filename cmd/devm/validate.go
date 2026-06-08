package main

import (
	"fmt"
	"os"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/render"
	"github.com/spf13/cobra"
)

var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate devm.yaml (and devm.me.yaml if present)",
	Long: `Validate devm.yaml (and devm.me.yaml if present), then lint
the spec.yaml that would be rendered from it. The lint step catches
render-layer bugs (e.g. unquoted YAML-reserved scalars) that would
otherwise only surface when sbx run tries to load the kit.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		cfg, err := config.Load(wd)
		if err != nil {
			return err
		}
		if err := render.LintRenderedSpec(cfg, wd); err != nil {
			return err
		}
		fmt.Printf("OK — %d service(s) configured\n", len(cfg.Services))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(validateCmd)
}
