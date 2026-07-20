package serviceapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"

	"github.com/mdubb86/devm/internal/identity"
)

// Client talks to the devm service over its Unix domain socket.
// Used by the CLI to check service health and version, and (in
// later ships) to call business endpoints.
type Client struct {
	httpClient *http.Client
}

// NewClient returns a Client targeting cfg.SocketPath() (honoring the
// legacy $DEVM_RUNTIME_DIR override — see SocketPath).
func NewClient(cfg identity.Config) *Client { return NewClientWithSocket(SocketPath(cfg)) }

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

// Version returns the build version string the service reports.
// Kept for backward compatibility; new callers should prefer
// BuildInfo to get commit + date alongside.
func (c *Client) Version(ctx context.Context) (string, error) {
	b, err := c.BuildInfo(ctx)
	if err != nil {
		return "", err
	}
	return b.Version, nil
}

// BuildInfo returns the full build identity the service reports
// (version + commit + date). Used by `just doctor` to detect when
// the daemon is running a different commit than the working tree.
func (c *Client) BuildInfo(ctx context.Context) (Build, error) {
	resp, err := c.do(ctx, "GET", "/version")
	if err != nil {
		return Build{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Build{}, fmt.Errorf("version request failed: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Build{}, err
	}
	var b Build
	if err := json.Unmarshal(body, &b); err != nil {
		return Build{}, fmt.Errorf("parse version response: %w", err)
	}
	return b, nil
}

// Handshake returns the daemon's build identity and, when name is
// non-empty, that project's iron-proxy health — one round-trip for the
// fingerprint-drift check plus the heal decision daemon-touching commands
// need to make.
func (c *Client) Handshake(ctx context.Context, name string) (HandshakeResponse, error) {
	resp, err := c.do(ctx, "GET", "/handshake?name="+url.QueryEscape(name))
	if err != nil {
		return HandshakeResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return HandshakeResponse{}, fmt.Errorf("handshake request failed: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return HandshakeResponse{}, err
	}
	var out HandshakeResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return HandshakeResponse{}, fmt.Errorf("parse handshake response: %w", err)
	}
	return out, nil
}

// StatusAll returns a cross-project status summary — one entry per
// project the daemon has a persisted StateSnapshot for, combining VM
// running state with iron-proxy health. Backs `devm status --all`.
func (c *Client) StatusAll(ctx context.Context) ([]ProjectStatus, error) {
	resp, err := c.do(ctx, "GET", "/status/all")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status/all request failed: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var out []ProjectStatus
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse status/all response: %w", err)
	}
	return out, nil
}

// Available returns true if the service is reachable. Used by CLI
// commands to decide whether to surface a warning, route through
// the service, etc. Errors are swallowed — "not available" is a
// normal state.
func (c *Client) Available(ctx context.Context) bool {
	return c.Health(ctx) == nil
}

// ProxyReady reports whether the daemon's reverse-proxy actor came
// up this daemon lifetime — i.e. launchd handed off the :80/:443
// listeners and NewProxyServer.Serve was launched. Returns false on
// any transport / decode error so callers can treat "can't tell" as
// "not ready" and render the CLI status accordingly.
//
// This replaces the older `net.DialTimeout("tcp", "127.0.0.1:443",
// ...)` probe. That probe dropped the connection mid-TLS handshake
// and each call spammed one "TLS handshake error … EOF" line into
// the daemon log — which is how we caught the bug in the first place.
func (c *Client) ProxyReady(ctx context.Context) (bool, error) {
	resp, err := c.do(ctx, "GET", "/proxy-status")
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false, fmt.Errorf("proxy-status: status %d", resp.StatusCode)
	}
	var body struct {
		Ready bool `json:"ready"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false, err
	}
	return body.Ready, nil
}

func (c *Client) do(ctx context.Context, method, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, nil)
	if err != nil {
		return nil, err
	}
	return c.httpClient.Do(req)
}
