package main

import (
	"os"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/router"
	"github.com/spf13/cobra"
)

var routeCmd = &cobra.Command{
	Use:   "route",
	Short: "Mac-side hostname routing via Caddy",
}

var routeLocalCmd = &cobra.Command{
	Use:   "local",
	Short: "Route hostnames to Mac canonical ports",
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
		return router.Apply(cmd.Context(), cfg, router.ModeLocal)
	},
}

var routeVMCmd = &cobra.Command{
	Use:   "vm",
	Short: "Route hostnames to sbx-published host ports",
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
		return router.Apply(cmd.Context(), cfg, router.ModeVM)
	},
}

var routeDownCmd = &cobra.Command{
	Use:   "down",
	Short: "Remove devm-owned routes for this project",
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
		return router.Down(cmd.Context(), cfg)
	},
}

func init() {
	routeCmd.AddCommand(routeLocalCmd, routeVMCmd, routeDownCmd)
	rootCmd.AddCommand(routeCmd)
}
