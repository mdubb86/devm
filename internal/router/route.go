package router

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/mdubb86/devm/internal/schema"
)

// Mode is the target state for `devm route`.
type Mode int

const (
	ModeLocal Mode = iota // route hostnames to Mac canonical ports
	ModeVM                // route hostnames to sbx-published host ports
)

func (m Mode) String() string {
	if m == ModeLocal {
		return "local"
	}
	return "vm"
}

// RoutingStatus is returned by Inspect for use by `devm status`.
type RoutingStatus struct {
	Proxy          string        `json:"proxy"`
	ProxyReachable bool          `json:"proxy_reachable"`
	Mode           string        `json:"mode"`
	Routes         []RouteStatus `json:"routes"`
}

// RouteStatus is one row of the status routing section.
type RouteStatus struct {
	Hostname string `json:"hostname"`
	Dial     string `json:"dial"`
	Mode     string `json:"mode"` // "local" | "vm" | "unknown"
	Resolves bool   `json:"resolves"`
}

// Apply is the production entry point for `devm route local|vm`.
func Apply(ctx context.Context, cfg schema.Config, mode Mode) error {
	resolver, err := NewResolver(cfg)
	if err != nil {
		return err
	}
	return apply(ctx, cfg, mode, New(), resolver)
}

// apply is the testable inner: caller injects client + resolver.
func apply(ctx context.Context, cfg schema.Config, mode Mode, client *Client, resolver Resolver) error {
	if proxy := proxyOf(cfg); proxy == "none" {
		fmt.Println("proxy disabled in project config (project.proxy: none)")
		return nil
	}
	mappings := mappingsFromCfg(cfg, mode)
	if len(mappings) == 0 {
		fmt.Println("no services with hostname+port; nothing to route")
		return nil
	}
	server, err := client.EnsureServer()
	if err != nil {
		return err
	}
	if err := client.Apply(server, cfg.Project.ID, mappings); err != nil {
		return err
	}
	fmt.Printf("Routing set to %s.\n", mode)
	for _, m := range mappings {
		fmt.Printf("  http://%s → localhost:%d\n", m.Hostname, m.DialPort)
	}
	hostnames := make([]string, len(mappings))
	for i, m := range mappings {
		hostnames[i] = m.Hostname
	}
	return resolver.Apply(ctx, hostnames)
}

// Down removes devm-owned routes for the project.
func Down(ctx context.Context, cfg schema.Config) error {
	resolver, err := NewResolver(cfg)
	if err != nil {
		return err
	}
	return down(ctx, cfg, New(), resolver)
}

func down(ctx context.Context, cfg schema.Config, client *Client, resolver Resolver) error {
	if proxy := proxyOf(cfg); proxy == "none" {
		fmt.Println("proxy disabled in project config (project.proxy: none)")
		return nil
	}
	hostnames := hostnamesOf(cfg)
	if err := client.Remove(cfg.Project.ID, hostnames); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Removed %d route(s) for %s.\n", len(hostnames), cfg.Project.ID)
	return resolver.Remove(ctx, hostnames)
}

// Inspect returns the current routing state for use by `devm status`.
func Inspect(ctx context.Context, cfg schema.Config) (RoutingStatus, error) {
	proxy := proxyOf(cfg)
	out := RoutingStatus{Proxy: proxy}
	if proxy == "none" {
		return out, nil
	}
	client := New()
	hostnames := hostnamesOf(cfg)
	entries, err := client.Inspect(cfg.Project.ID, hostnames)
	if err != nil {
		out.ProxyReachable = false
		return out, nil
	}
	out.ProxyReachable = true

	if len(entries) == 0 {
		out.Mode = ""
		return out, nil
	}

	// Classify each route as local/vm/unknown by dial port.
	canonical := map[string]int{}
	vmPort := map[string]int{}
	for _, svc := range cfg.Services {
		if svc.Port == 0 || svc.Hostname == "" {
			continue
		}
		canonical[svc.Hostname] = svc.Port
		vmPort[svc.Hostname] = cfg.Project.PortOffset + svc.Port
	}

	modes := map[string]int{}
	for _, e := range entries {
		wantVM := fmt.Sprintf("localhost:%d", vmPort[e.Hostname])
		wantLocal := fmt.Sprintf("localhost:%d", canonical[e.Hostname])
		m := "unknown"
		if e.Dial == wantVM {
			m = "vm"
		} else if e.Dial == wantLocal {
			m = "local"
		}
		modes[m]++
		out.Routes = append(out.Routes, RouteStatus{
			Hostname: e.Hostname,
			Dial:     e.Dial,
			Mode:     m,
		})
	}
	// Aggregate mode.
	if modes["local"] == len(out.Routes) {
		out.Mode = "local"
	} else if modes["vm"] == len(out.Routes) {
		out.Mode = "vm"
	} else {
		out.Mode = "mixed (drift)"
	}

	// DNS resolution check.
	unresolved := map[string]bool{}
	for _, h := range CheckResolution(hostnames) {
		unresolved[h] = true
	}
	for i := range out.Routes {
		out.Routes[i].Resolves = !unresolved[out.Routes[i].Hostname]
	}
	return out, nil
}

// --- helpers ---

func proxyOf(cfg schema.Config) string {
	if cfg.Project.Proxy == "" {
		return "caddy"
	}
	return cfg.Project.Proxy
}

func hostnamesOf(cfg schema.Config) []string {
	var out []string
	for _, svc := range cfg.Services {
		if svc.Hostname != "" {
			out = append(out, svc.Hostname)
		}
	}
	sort.Strings(out)
	return out
}

// mappingsFromCfg enumerates HostMappings from cfg.Services. Services
// without a hostname OR without a port are skipped. Sort by hostname
// for deterministic apply order.
func mappingsFromCfg(cfg schema.Config, mode Mode) []HostMapping {
	var out []HostMapping
	for _, svc := range cfg.Services {
		if svc.Hostname == "" || svc.Port == 0 {
			continue
		}
		dial := svc.Port
		if mode == ModeVM {
			dial = cfg.Project.PortOffset + svc.Port
		}
		out = append(out, HostMapping{Hostname: svc.Hostname, DialPort: dial})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hostname < out[j].Hostname })
	return out
}
