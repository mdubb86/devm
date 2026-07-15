package serviceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
)

// ApplyRoutes pushes the project's routes to the daemon. Replaces
// any previous set for this project.
func (c *Client) ApplyRoutes(ctx context.Context, name string, routes []Route) error {
	body, err := json.Marshal(ApplyRequest{Name: name, Routes: routes})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST",
		"http://localhost/routes/apply", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("routes/apply: status %d", resp.StatusCode)
	}
	return nil
}

// RemoveRoutes drops the project's routes.
func (c *Client) RemoveRoutes(ctx context.Context, name string) error {
	body, err := json.Marshal(RemoveRequest{Name: name})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST",
		"http://localhost/routes/remove", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("routes/remove: status %d", resp.StatusCode)
	}
	return nil
}

// RoutingStatusFromDaemon queries the daemon's /routes endpoint and
// composes a RoutingStatus suitable for `devm status` rendering.
// The daemon being reachable implies the proxy is alive; we report
// "proxy: devm" (instead of "caddy") and ProxyReachable: true.
//
// When the daemon is down or returns an error, the caller should
// substitute a zero RoutingStatus (Proxy: "" + ProxyReachable: false)
// — the formatRouting code handles that as "unreachable".
func (c *Client) RoutingStatusFromDaemon(ctx context.Context) (RoutingStatus, error) {
	routes, err := c.ListRoutes(ctx)
	if err != nil {
		return RoutingStatus{}, err
	}
	out := RoutingStatus{
		Proxy:          "devm",
		ProxyReachable: true,
	}
	// Flatten per-project routes into a single ordered slice; figure
	// out the dominant mode for the "mode:" line. If routes mix
	// modes across projects, label as "mixed (drift)".
	modes := make(map[RouteMode]int)
	// Collect project IDs for deterministic ordering.
	projIDs := make([]string, 0, len(routes))
	for id := range routes {
		projIDs = append(projIDs, id)
	}
	sort.Strings(projIDs)
	for _, projID := range projIDs {
		for _, r := range routes[projID] {
			modes[r.Mode]++
			out.Routes = append(out.Routes, RouteStatus{
				Hostname: r.Hostname,
				Dial:     fmt.Sprintf("localhost:%d", r.BackendPort),
				Mode:     r.Mode.String(),
			})
		}
	}
	switch {
	case len(out.Routes) == 0:
		out.Mode = ""
	case len(modes) == 1:
		for m := range modes {
			out.Mode = m.String()
		}
	default:
		out.Mode = "mixed (drift)"
	}
	return out, nil
}

// ListRoutes returns all routes the daemon knows about, keyed by
// project_id.
func (c *Client) ListRoutes(ctx context.Context) (map[string][]Route, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		"http://localhost/routes", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("routes: status %d", resp.StatusCode)
	}
	var out map[string][]Route
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}
