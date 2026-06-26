package serviceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// ApplyRoutes pushes the project's routes to the daemon. Replaces
// any previous set for this project.
func (c *Client) ApplyRoutes(ctx context.Context, projectID string, routes []Route) error {
	body, err := json.Marshal(ApplyRequest{ProjectID: projectID, Routes: routes})
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
func (c *Client) RemoveRoutes(ctx context.Context, projectID string) error {
	body, err := json.Marshal(RemoveRequest{ProjectID: projectID})
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
