package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi"
)

var routeCmd = &cobra.Command{
	Use:   "route",
	Short: "Mac-side hostname routing (managed by the devm daemon)",
}

var routeLocalCmd = &cobra.Command{
	Use:   "local",
	Short: "Route hostnames to Mac canonical ports (dev server on host)",
	RunE:  applyRoute(serviceapi.ModeLocal),
}

var routeVMCmd = &cobra.Command{
	Use:   "vm",
	Short: "Route hostnames to sbx-published ports (service in sandbox)",
	RunE:  applyRoute(serviceapi.ModeVM),
}

func applyRoute(mode serviceapi.RouteMode) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		repoRoot, err := os.Getwd()
		if err != nil {
			return err
		}
		cfg, err := config.Load(repoRoot)
		if err != nil {
			return err
		}
		routes := buildRoutes(cfg, mode)
		if len(routes) == 0 {
			fmt.Println("no services with hostname+port; nothing to route")
			return nil
		}
		c := serviceapi.NewClient()
		ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
		defer cancel()
		if err := c.ApplyRoutes(ctx, cfg.Project.ID, routes); err != nil {
			return fmt.Errorf("apply routes: %w", err)
		}
		fmt.Printf("Routing set to %s.\n", mode)
		for _, r := range routes {
			fmt.Printf("  http(s)://%s → localhost:%d\n", r.Hostname, r.BackendPort)
		}
		return nil
	}
}

// buildRoutes extracts Routes from the project config in the
// requested mode. Mode determines whether BackendPort is the
// service's in-VM canonical port (local) or the sbx-published host
// port (vm). Package-private but accessible from any file in
// cmd/devm/; Task 10 (shell.go) reuses it.
func buildRoutes(cfg schema.Config, mode serviceapi.RouteMode) []serviceapi.Route {
	var out []serviceapi.Route
	for _, svc := range cfg.Services {
		if svc.Hostname == "" || svc.Port == 0 {
			continue
		}
		backendPort := svc.Port
		out = append(out, serviceapi.Route{
			Hostname:    svc.Hostname,
			BackendPort: backendPort,
			Mode:        mode,
		})
	}
	return out
}

func init() {
	routeCmd.AddCommand(routeLocalCmd)
	routeCmd.AddCommand(routeVMCmd)
	rootCmd.AddCommand(routeCmd)
}
