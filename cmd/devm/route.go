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
	Short: "Route hostnames to VM service ports (service in VM)",
	RunE:  applyRoute(serviceapi.ModeVM),
}

func applyRoute(mode serviceapi.RouteMode) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		if err := requireDaemonInSync(cmd.Context()); err != nil {
			return err
		}
		ident := cfg // capture package identity cfg before it's shadowed below
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
		c := serviceapi.NewClient(ident)
		ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
		defer cancel()
		resolved, err := c.ApplyRoutes(ctx, cfg.Project.Name, routes)
		if err != nil {
			return fmt.Errorf("apply routes: %w", err)
		}
		fmt.Printf("Routing set to %s.\n", mode)
		for _, r := range resolved {
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
// requested mode. In vm mode, a proxied (non-direct) service's
// BackendHost is left unset: the daemon substitutes the project's
// allocated IP at /routes/apply time.
func buildRoutes(cfg schema.Config, mode serviceapi.RouteMode) ([]serviceapi.Route, error) {
	var out []serviceapi.Route

	for _, svc := range cfg.Services {
		if svc.Hostname == "" || svc.Port == 0 {
			continue
		}
		route := serviceapi.Route{
			Hostname:    svc.Hostname,
			BackendPort: svc.Port,
			Mode:        mode,
			Project:     cfg.Project.Name,
			ExposeHost:  svc.ExposeHost,
		}
		// For mode==vm && !svc.Direct, the daemon substitutes BackendHost with
		// the project's allocated projectIP at /routes/apply time (based on
		// Mode + Direct fields — see internal/serviceapi/routes.go). We leave
		// BackendHost as zero-value here on purpose.
		if svc.Direct {
			// Direct services are DNS-only: no backend to dial.
			route.Direct = true
		}
		// mode == ModeLocal + !Direct: BackendHost stays "" → daemon's
		// ProxyServer default of "localhost" (proxy.go) resolves it. That's
		// correct for local-mode services running on the Mac.
		out = append(out, route)
	}
	return out, nil
}

func init() {
	routeCmd.AddCommand(routeLocalCmd)
	routeCmd.AddCommand(routeVMCmd)
	rootCmd.AddCommand(routeCmd)
}
