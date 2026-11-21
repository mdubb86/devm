package serviceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/snapshot"
)

// Client talks to the devm service over its Unix domain socket.
// Used by the CLI to check service health and version, and (in
// later ships) to call business endpoints.
type Client struct {
	httpClient *http.Client
}

// NewClient returns a Client targeting cfg.SocketPath().
func NewClient(cfg identity.Config) *Client { return NewClientWithSocket(cfg.SocketPath()) }

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

// Workspaces returns the daemon's workspace registry — one entry per
// project with a primary repo, pairing its guest clone path with the
// Mac-side volume storage path.
func (c *Client) Workspaces(ctx context.Context) ([]WorkspaceEntry, error) {
	resp, err := c.do(ctx, "GET", "/workspaces")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("workspaces request failed: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var out []WorkspaceEntry
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse workspaces response: %w", err)
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

func (c *Client) do(ctx context.Context, method, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, nil)
	if err != nil {
		return nil, err
	}
	return c.httpClient.Do(req)
}

// doJSON issues method path with body JSON-encoded and Content-Type set.
// Used by the snapshot helpers whose daemon endpoints take a JSON body.
func (c *Client) doJSON(ctx context.Context, method, path string, body any) (*http.Response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode %s body: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.httpClient.Do(req)
}

// snapshotError is the JSON body the daemon returns on a 4xx/5xx.
type snapshotError struct {
	Error string `json:"error"`
}

// readSnapshotError decodes the daemon's {"error": "..."} body. Falls
// back to the raw body text when the JSON is unparseable so an
// unexpected 500 still surfaces something useful.
func readSnapshotError(resp *http.Response) string {
	body, _ := io.ReadAll(resp.Body)
	var e snapshotError
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return e.Error
	}
	return string(bytes.TrimSpace(body))
}

// SnapshotCreate asks the daemon to capture a snapshot of the named
// project's entry. Returns the resulting Meta on success.
func (c *Client) SnapshotCreate(ctx context.Context, project, entry string, source snapshot.Source) (snapshot.Meta, error) {
	resp, err := c.doJSON(ctx, http.MethodPost, "/snapshot/create", map[string]string{
		"project": project,
		"entry":   entry,
		"source":  string(source),
	})
	if err != nil {
		return snapshot.Meta{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return snapshot.Meta{}, fmt.Errorf("%s", readSnapshotError(resp))
	}
	var m snapshot.Meta
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return snapshot.Meta{}, fmt.Errorf("decode create response: %w", err)
	}
	return m, nil
}

// SnapshotRestore replays a snapshot into the guest. An empty ts means
// "latest"; the daemon resolves it. force=true bypasses the mode/path
// drift refusal — the operator has told us "yes, replay this archive
// into the current yaml target even though the archive was captured
// under a different declaration". The boolean return names what
// happened: true if an archive was found and replayed, false if there
// was nothing to restore. A false with nil error is the first-boot
// case install hooks rely on.
func (c *Client) SnapshotRestore(ctx context.Context, project, entry, ts string, force bool) (bool, error) {
	resp, err := c.doJSON(ctx, http.MethodPost, "/snapshot/restore", map[string]any{
		"project": project,
		"entry":   entry,
		"ts":      ts,
		"force":   force,
	})
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("%s", readSnapshotError(resp))
	}
	var body struct {
		Restored bool `json:"restored"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false, fmt.Errorf("decode restore response: %w", err)
	}
	return body.Restored, nil
}

// SnapshotClear removes every archive and sidecar under the entry's
// directory. Returns the number of *.zst archives removed; a never-
// captured entry returns (0, nil).
func (c *Client) SnapshotClear(ctx context.Context, project, entry string) (int, error) {
	resp, err := c.doJSON(ctx, http.MethodPost, "/snapshot/clear", map[string]string{
		"project": project,
		"entry":   entry,
	})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%s", readSnapshotError(resp))
	}
	var body struct {
		Removed int `json:"removed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, fmt.Errorf("decode clear response: %w", err)
	}
	return body.Removed, nil
}

// SnapshotList returns archive metas. An empty entry asks the daemon to
// return the newest archive per declared entry; otherwise it returns
// every archive under that one entry, newest first.
func (c *Client) SnapshotList(ctx context.Context, project, entry string) ([]snapshot.Meta, error) {
	q := url.Values{}
	q.Set("project", project)
	if entry != "" {
		q.Set("entry", entry)
	}
	resp, err := c.do(ctx, http.MethodGet, "/snapshot/list?"+q.Encode())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", readSnapshotError(resp))
	}
	var metas []snapshot.Meta
	if err := json.NewDecoder(resp.Body).Decode(&metas); err != nil {
		return nil, fmt.Errorf("decode list response: %w", err)
	}
	return metas, nil
}

// SnapshotPrune trims the entry down to keep archives, newest first.
// Returns the timestamps the daemon removed.
func (c *Client) SnapshotPrune(ctx context.Context, project, entry string, keep int) ([]string, error) {
	resp, err := c.doJSON(ctx, http.MethodPost, "/snapshot/prune", map[string]any{
		"project": project,
		"entry":   entry,
		"keep":    keep,
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", readSnapshotError(resp))
	}
	var body struct {
		Removed []string `json:"removed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode prune response: %w", err)
	}
	return body.Removed, nil
}

// SnapshotShow returns one archive's Meta. An empty ts means "latest";
// the daemon resolves it.
func (c *Client) SnapshotShow(ctx context.Context, project, entry, ts string) (snapshot.Meta, error) {
	q := url.Values{}
	q.Set("project", project)
	q.Set("entry", entry)
	q.Set("ts", ts)
	resp, err := c.do(ctx, http.MethodGet, "/snapshot/show?"+q.Encode())
	if err != nil {
		return snapshot.Meta{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return snapshot.Meta{}, fmt.Errorf("%s", readSnapshotError(resp))
	}
	var m snapshot.Meta
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return snapshot.Meta{}, fmt.Errorf("decode show response: %w", err)
	}
	return m, nil
}
