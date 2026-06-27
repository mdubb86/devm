package serviceapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
)

// Client talks to the devm service over its Unix domain socket.
// Used by the CLI to check service health and version, and (in
// later ships) to call business endpoints.
type Client struct {
	httpClient *http.Client
}

// NewClient returns a Client targeting the default SocketPath().
func NewClient() *Client { return NewClientWithSocket(SocketPath()) }

// NewClientWithSocket returns a Client targeting the given socket.
// Tests use this with a temp socket.
//
// No client-level Timeout — long-running endpoints like /vm/start
// (which clones+boots a VM and runs provisioning) can take minutes.
// Per-request callers control timeout via context.WithTimeout.
func NewClientWithSocket(socketPath string) *Client {
	return &Client{
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
				},
			},
		},
	}
}

// Health returns nil if the service is up and responsive.
func (c *Client) Health(ctx context.Context) error {
	resp, err := c.do(ctx, "GET", "/health")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("service unhealthy: status %d", resp.StatusCode)
	}
	return nil
}

// Version returns the build version the service reports.
func (c *Client) Version(ctx context.Context) (string, error) {
	resp, err := c.do(ctx, "GET", "/version")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("version request failed: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return "", fmt.Errorf("parse version response: %w", err)
	}
	return v.Version, nil
}

// Available returns true if the service is reachable. Used by CLI
// commands to decide whether to surface a warning, route through
// the service, etc. Errors are swallowed — "not available" is a
// normal state.
func (c *Client) Available(ctx context.Context) bool {
	return c.Health(ctx) == nil
}

func (c *Client) do(ctx context.Context, method, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, nil)
	if err != nil {
		return nil, err
	}
	return c.httpClient.Do(req)
}
