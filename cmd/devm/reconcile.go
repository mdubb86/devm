package main

import (
	"fmt"
	"os"

	"github.com/mtwaage/devm/internal/config"
	"github.com/mtwaage/devm/internal/render"
	"github.com/spf13/cobra"
)

var reconcileCmd = &cobra.Command{
	Use:   "reconcile",
	Short: "Regenerate .devm/ cache (sbx kit assets + Caddyfile + scripts)",
	RunE: func(cmd *cobra.Command, args []string) error {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		cfg, err := config.Load(wd)
		if err != nil {
			return err
		}
		if err := render.WriteDevmDir(cfg, wd); err != nil {
			return err
		}
		fmt.Println("OK — .devm/ regenerated")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(reconcileCmd)
}
