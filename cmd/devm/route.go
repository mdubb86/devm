package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/sandbox/tart"
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
	Short: "Route hostnames to VM service ports (service in VM)",
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
		routes, err := buildRoutes(cfg, mode)
		if err != nil {
			return err
		}
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
			host := r.BackendHost
			if host == "" {
				host = "localhost"
			}
			fmt.Printf("  http(s)://%s → %s:%d\n", r.Hostname, host, r.BackendPort)
		}
		return nil
	}
}

// buildRoutes extracts Routes from the project config in the
// requested mode. For vm mode, resolves the VM's IP via tart so the
// proxy dials the VM directly instead of localhost.
func buildRoutes(cfg schema.Config, mode serviceapi.RouteMode) ([]serviceapi.Route, error) {
	var out []serviceapi.Route
	var vmIP string

	if mode == serviceapi.ModeVM {
		tr := tart.New()
		ip, err := tr.IP(context.Background(), cfg.Project.SandboxName)
		if err != nil {
			return nil, fmt.Errorf("get vm ip (is the VM running? `devm shell` first): %w", err)
		}
		vmIP = ip
	}

	for _, svc := range cfg.Services {
		if svc.Hostname == "" || svc.Port == 0 {
			continue
		}
		route := serviceapi.Route{
			Hostname:    svc.Hostname,
			BackendPort: svc.Port,
			Mode:        mode,
		}
		if mode == serviceapi.ModeVM {
			route.BackendHost = vmIP
		}
		out = append(out, route)
	}
	return out, nil
}

func init() {
	routeCmd.AddCommand(routeLocalCmd)
	routeCmd.AddCommand(routeVMCmd)
	rootCmd.AddCommand(routeCmd)
}
