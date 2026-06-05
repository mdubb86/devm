package main

import (
	"fmt"
	"os"

	"github.com/mtwaage/devm/internal/config"
	"github.com/spf13/cobra"
)

var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate devm.yaml (and devm.me.yaml if present)",
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
		fmt.Printf("OK — %d service(s) configured\n", len(cfg.Services))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(validateCmd)
}
