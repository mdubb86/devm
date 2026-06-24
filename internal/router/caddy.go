// Package router implements Mac-side hostname routing via the Caddy
// admin API plus /etc/hosts resolution (snippet print or localias).
package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DefaultCaddyURL is where Caddy's admin API listens by default.
const DefaultCaddyURL = "http://localhost:2019"

// ServerID tags the Caddy HTTP server devm creates when no other
// server is already bound to :80. Used as the @id so subsequent
// runs know whether we own the server.
const ServerID = "devm.server"

// Client is a thin caddy admin-API client.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New returns a Client targeting DefaultCaddyURL.
func New() *Client { return NewWithURL(DefaultCaddyURL) }

// NewWithURL returns a Client targeting the given URL.
func NewWithURL(url string) *Client {
	return &Client{baseURL: url, httpClient: &http.Client{Timeout: 5 * time.Second}}
}

// do issues an HTTP request and returns (status, body, err). Transport
// errors return a typed message that points the user at brew install.
func (c *Client) do(method, path string, body any) (int, string, error) {
	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return 0, "", fmt.Errorf("marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, c.baseURL+path, reqBody)
	if err != nil {
		return 0, "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf(
			"caddy admin API not reachable at %s:\n  reason: %v\n\n"+
				"Start Caddy as a service:\n  brew install caddy\n  sudo brew services start caddy",
			c.baseURL, err)
	}
	defer resp.Body.Close()
	bs, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(bs), nil
}

// EnsureServer returns the name of an HTTP server listening on :80.
// If one exists already (any project, or the user's own setup), use
// it. If none exists, create one tagged @id=devm.server.
func (c *Client) EnsureServer() (string, error) {
	status, body, err := c.do("GET", "/config/apps/http/servers", nil)
	if err != nil {
		return "", err
	}
	if status == 404 {
		body = "{}" // apps.http missing entirely; we'll create devm anyway
	} else if status >= 300 {
		return "", fmt.Errorf("GET servers: %d %s", status, body)
	}

	var servers map[string]struct {
		Listen []string `json:"listen"`
	}
	if body != "" {
		if err := json.Unmarshal([]byte(body), &servers); err != nil {
			return "", fmt.Errorf("parse servers: %w", err)
		}
	}
	for name, srv := range servers {
		for _, listen := range srv.Listen {
			if listenMatchesPort80(listen) {
				return name, nil
			}
		}
	}

	body2 := map[string]any{
		"@id":             ServerID,
		"listen":          []string{":80"},
		"automatic_https": map[string]any{"disable": true},
		"routes":          []any{},
	}
	status, text, err := c.do("PUT", "/config/apps/http/servers/devm", body2)
	if err != nil {
		return "", err
	}
	if status >= 300 {
		return "", fmt.Errorf("PUT devm server: %d %s", status, text)
	}
	return "devm", nil
}

// listenMatchesPort80 returns true for any listen spec that binds :80,
// including bare ":80", "0.0.0.0:80", "127.0.0.1:80", "[::]:80".
func listenMatchesPort80(s string) bool {
	return len(s) >= 3 && s[len(s)-3:] == ":80"
}

// HostMapping is a single hostname → dial-port mapping for Caddy.
type HostMapping struct {
	Hostname string
	DialPort int
}

const routeIDPrefix = "devm" // full prefix is devm.<projectID>.route.<hostname>

func routeID(projectID, hostname string) string {
	return fmt.Sprintf("%s.%s.route.%s", routeIDPrefix, projectID, hostname)
}

// Remove deletes all devm-owned routes for the given projectID. If
// devm.server exists and no devm-owned routes remain in any server,
// also delete devm.server.
func (c *Client) Remove(projectID string, hostnames []string) error {
	for _, h := range hostnames {
		id := routeID(projectID, h)
		// Best-effort: tolerate 404 (already gone).
		c.do("DELETE", "/id/"+id, nil)
	}
	// Check if we own devm.server.
	ownStatus, _, _ := c.do("GET", "/id/"+ServerID, nil)
	if ownStatus >= 300 {
		return nil // we don't own the server
	}
	// We own it; check whether any devm.*.route.* @ids still exist
	// across all servers.
	_, body, err := c.do("GET", "/config/apps/http/servers", nil)
	if err != nil {
		return err
	}
	var servers map[string]struct {
		Routes []struct {
			ID string `json:"@id"`
		} `json:"routes"`
	}
	if err := json.Unmarshal([]byte(body), &servers); err != nil {
		return fmt.Errorf("parse servers for cleanup: %w", err)
	}
	for _, srv := range servers {
		for _, r := range srv.Routes {
			if r.ID != "" && len(r.ID) >= len(routeIDPrefix)+1 &&
				r.ID[:len(routeIDPrefix)+1] == routeIDPrefix+"." {
				return nil // a devm route still exists; keep the server
			}
		}
	}
	c.do("DELETE", "/id/"+ServerID, nil)
	return nil
}

// Apply registers the given hostname → dial-port mappings in the
// named server's routes. For each mapping, if a route with the
// matching @id already exists, PATCH it in place; otherwise POST a
// new one to the server's routes array.
func (c *Client) Apply(serverName, projectID string, mappings []HostMapping) error {
	for _, m := range mappings {
		id := routeID(projectID, m.Hostname)
		body := map[string]any{
			"@id":   id,
			"match": []map[string]any{{"host": []string{m.Hostname}}},
			"handle": []map[string]any{{
				"handler": "reverse_proxy",
				"upstreams": []map[string]any{
					{"dial": fmt.Sprintf("localhost:%d", m.DialPort)},
				},
			}},
			"terminal": true,
		}
		// Check existence by @id.
		existsStatus, _, err := c.do("GET", "/id/"+id, nil)
		if err != nil {
			return err
		}
		if existsStatus < 300 {
			status, text, err := c.do("PATCH", "/id/"+id, body)
			if err != nil {
				return err
			}
			if status >= 300 {
				return fmt.Errorf("PATCH route %s: %d %s", id, status, text)
			}
			continue
		}
		// POST to append.
		status, text, err := c.do("POST",
			"/config/apps/http/servers/"+serverName+"/routes", body)
		if err != nil {
			return err
		}
		if status >= 300 {
			return fmt.Errorf("POST route %s: %d %s", id, status, text)
		}
	}
	return nil
}
