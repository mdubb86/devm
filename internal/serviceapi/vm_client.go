package serviceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/mdubb86/devm/internal/schema"
)

// StartVM asks the daemon to clone (if absent) and start the project VM.
func (c *Client) StartVM(ctx context.Context, req VMStartRequest) (VMStartResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return VMStartResponse{}, err
	}
	r, err := c.post(ctx, "/vm/start", body)
	if err != nil {
		return VMStartResponse{}, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(r.Body)
		return VMStartResponse{}, fmt.Errorf("vm/start: status %d: %s", r.StatusCode, strings.TrimSpace(string(msg)))
	}
	var resp VMStartResponse
	if err := json.NewDecoder(r.Body).Decode(&resp); err != nil {
		return VMStartResponse{}, err
	}
	return resp, nil
}

// EnforcementConfig asks the daemon whether this project's iron-proxy
// state exists (404/412 if /vm/start was never called). It's a
// precondition check the orchestrator calls before provisioning
// proceeds. It used to also return the guest-side timesyncd NTP config;
// that's now baked into the base image (image/provision-base.sh), so
// the response body carries nothing.
func (c *Client) EnforcementConfig(ctx context.Context, name string) (VMEnforcementConfigResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		"http://localhost/vm/enforcement-config?name="+name, nil)
	if err != nil {
		return VMEnforcementConfigResponse{}, err
	}
	r, err := c.httpClient.Do(req)
	if err != nil {
		return VMEnforcementConfigResponse{}, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(r.Body)
		return VMEnforcementConfigResponse{}, fmt.Errorf("vm/enforcement-config: status %d: %s", r.StatusCode, strings.TrimSpace(string(msg)))
	}
	var resp VMEnforcementConfigResponse
	if err := json.NewDecoder(r.Body).Decode(&resp); err != nil {
		return VMEnforcementConfigResponse{}, err
	}
	return resp, nil
}

// StopVM asks the daemon to stop the project VM. The daemon calls
// `tart stop <name>` first so the guest gets a graceful shutdown before
// the tart-run process is signalled.
func (c *Client) StopVM(ctx context.Context, name string) error {
	body, err := json.Marshal(VMStopRequest{Name: name})
	if err != nil {
		return err
	}
	r, err := c.post(ctx, "/vm/stop", body)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusNoContent {
		return fmt.Errorf("vm/stop: status %d", r.StatusCode)
	}
	return nil
}

// PassthroughEgress calls POST /vm/passthrough-egress, flipping the
// project's authority mode to passthrough for a bounded window (iron-proxy
// remains in the traffic path, MITM'ing + audit-logging + secret-substituting).
// durationSeconds <= 0 asks the daemon to apply defaultPassthroughSeconds (30).
// Returns whether the project had an existing open window (drives "opened" vs
// "renewed" user message) and the seconds the daemon actually armed.
func (c *Client) PassthroughEgress(ctx context.Context, name string, durationSeconds int) (wasOpen bool, expiresSeconds int, err error) {
	body, err := json.Marshal(VMEgressPassthroughRequest{Name: name, DurationSeconds: durationSeconds})
	if err != nil {
		return false, 0, err
	}
	r, err := c.post(ctx, "/vm/passthrough-egress", body)
	if err != nil {
		return false, 0, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(r.Body)
		return false, 0, fmt.Errorf("vm/passthrough-egress: status %d: %s", r.StatusCode, strings.TrimSpace(string(msg)))
	}
	var resp VMEgressPassthroughResponse
	if err := json.NewDecoder(r.Body).Decode(&resp); err != nil {
		return false, 0, err
	}
	return resp.WasOpen, resp.ExpiresSeconds, nil
}

// RestrictEgress calls POST /vm/restrict-egress, restoring the
// authority mode to restricted (softnet stays FORWARDING; iron-proxy
// stays in the path) and cancelling any pending passthrough restore
// timer. Returns whether there was a window to restrict — false is
// not an error; the CLI reports it as a no-op.
func (c *Client) RestrictEgress(ctx context.Context, name string) (wasOpen bool, err error) {
	body, err := json.Marshal(VMProjectRequest{Name: name})
	if err != nil {
		return false, err
	}
	r, err := c.post(ctx, "/vm/restrict-egress", body)
	if err != nil {
		return false, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(r.Body)
		return false, fmt.Errorf("vm/restrict-egress: status %d: %s", r.StatusCode, strings.TrimSpace(string(msg)))
	}
	var resp VMEgressRestrictResponse
	if err := json.NewDecoder(r.Body).Decode(&resp); err != nil {
		return false, err
	}
	return resp.WasOpen, nil
}

// EgressStatus queries the daemon's egress policy state for the
// project. Returns "restricted" when no passthrough window is
// active, "passthrough" with a non-nil expiry when one is. Returns
// nil (not an error) on a 404 — the project has no tracked state.
func (c *Client) EgressStatus(ctx context.Context, name string) (*EgressStatus, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://localhost/vm/egress-status?name="+name, nil)
	if err != nil {
		return nil, err
	}
	r, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(r.Body)
		return nil, fmt.Errorf("vm/egress-status: status %d: %s", r.StatusCode, strings.TrimSpace(string(msg)))
	}
	var resp EgressStatus
	if err := json.NewDecoder(r.Body).Decode(&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Reconcile calls POST /vm/reconcile with cfg + workspace_host_path.
// The daemon diffs cfg against the project's last-applied snapshot,
// applies every live-bucket change in place, and returns what still
// requires a VM recreate (teardown_required).
func (c *Client) Reconcile(ctx context.Context, req VMReconcileRequest) (VMReconcileResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return VMReconcileResponse{}, err
	}
	r, err := c.post(ctx, "/vm/reconcile", body)
	if err != nil {
		return VMReconcileResponse{}, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(r.Body)
		return VMReconcileResponse{}, fmt.Errorf("vm/reconcile: status %d: %s", r.StatusCode, strings.TrimSpace(string(msg)))
	}
	var resp VMReconcileResponse
	if err := json.NewDecoder(r.Body).Decode(&resp); err != nil {
		return VMReconcileResponse{}, err
	}
	return resp, nil
}

// ApplyIronProxy calls POST /vm/apply-iron-proxy with the freshly
// resolved allowlist and secrets. The daemon regenerates the
// per-project iron-proxy config on the SAME MAC_HOST:port as the
// pre-existing config on disk, restarts iron-proxy if it was
// running, or spawns it if the config existed but iron-proxy was
// dead. Returns VMApplyIronProxyResponse.VMRunning=false when there
// is no iron-proxy config file (VM has never started).
func (c *Client) ApplyIronProxy(ctx context.Context, req VMApplyIronProxyRequest) (VMApplyIronProxyResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return VMApplyIronProxyResponse{}, err
	}
	r, err := c.post(ctx, "/vm/apply-iron-proxy", body)
	if err != nil {
		return VMApplyIronProxyResponse{}, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(r.Body)
		return VMApplyIronProxyResponse{}, fmt.Errorf("vm/apply-iron-proxy: status %d: %s", r.StatusCode, strings.TrimSpace(string(msg)))
	}
	var resp VMApplyIronProxyResponse
	if err := json.NewDecoder(r.Body).Decode(&resp); err != nil {
		return VMApplyIronProxyResponse{}, err
	}
	return resp, nil
}

// BeginProvisioning calls POST /vm/begin-provisioning, flipping the
// project's softnet control socket to ENFORCED-behavior (:80/:443 route
// to iron-proxy) and the egress policy authority to ModePassthrough.
// Called post-RunBundle (the guest trust store already has the
// iron-proxy CA) and pre-RunUser — iron-proxy is in the traffic path
// for the rest of the VM's life from this call onward.
func (c *Client) BeginProvisioning(ctx context.Context, name string) error {
	body, err := json.Marshal(VMProjectRequest{Name: name})
	if err != nil {
		return err
	}
	r, err := c.post(ctx, "/vm/begin-provisioning", body)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(r.Body)
		return fmt.Errorf("vm/begin-provisioning: status %d: %s", r.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// VolumeSync calls POST /vm/volume-sync, establishing a mutagen sync
// session for every entity in cfg (volumes and repos alike). Called
// after BeginProvisioning and before RepoClone.
func (c *Client) VolumeSync(ctx context.Context, name string, cfg schema.Config, repoRoot string) error {
	body, err := json.Marshal(VMVolumeSyncRequest{Name: name, RepoRoot: repoRoot, Cfg: cfg})
	if err != nil {
		return err
	}
	r, err := c.post(ctx, "/vm/volume-sync", body)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(r.Body)
		return fmt.Errorf("vm/volume-sync: status %d: %s", r.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// RepoClone calls POST /vm/repo-clone, running a cold-start git clone
// through iron-proxy for every repo entity in cfg where the relevant
// sides are empty. Called after VolumeSync. tunnelPort is iron-proxy's
// CONNECT-capable tunnel_listen port, needed to build the guest-visible
// HTTP_PROXY URL.
func (c *Client) RepoClone(ctx context.Context, name string, cfg schema.Config, repoRoot string, tunnelPort int) error {
	body, err := json.Marshal(VMRepoCloneRequest{Name: name, RepoRoot: repoRoot, Cfg: cfg, TunnelPort: tunnelPort})
	if err != nil {
		return err
	}
	r, err := c.post(ctx, "/vm/repo-clone", body)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(r.Body)
		return fmt.Errorf("vm/repo-clone: status %d: %s", r.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// EndProvisioning calls POST /vm/end-provisioning, flipping the egress
// policy authority back to ModeRestricted. Called pre-RunEnforced.
// Softnet stays in ENFORCED-behavior — iron-proxy remains in the
// traffic path for the rest of the VM's life; only the authority mode
// changes here.
func (c *Client) EndProvisioning(ctx context.Context, name string) error {
	body, err := json.Marshal(VMProjectRequest{Name: name})
	if err != nil {
		return err
	}
	r, err := c.post(ctx, "/vm/end-provisioning", body)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(r.Body)
		return fmt.Errorf("vm/end-provisioning: status %d: %s", r.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// SetAllowlist calls POST /vm/set-allowlist to replace the project's
// full allowlist on the daemon's PolicyAuthority. Takes effect on the
// next request through iron-proxy — no respawn, no denial-counter
// reset. Used by reconcile's BucketLive dispatch for KindNetworkAdd/
// KindNetworkRemove.
func (c *Client) SetAllowlist(ctx context.Context, name string, allowlist []string) error {
	body, err := json.Marshal(VMSetAllowlistRequest{Name: name, Allowlist: allowlist})
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	r, err := c.post(ctx, "/vm/set-allowlist", body)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(r.Body)
		return fmt.Errorf("vm/set-allowlist: status %d: %s", r.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// Denials queries the daemon for iron-proxy allow-list rejects observed
// this iron-proxy lifetime, per host. Sorted by count desc. Empty slice
// (never nil) if the project hasn't triggered any denials yet, iron-proxy
// isn't running, or the daemon wasn't built with tracking wired.
func (c *Client) Denials(ctx context.Context, name string) ([]Denial, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		"http://localhost/denials?name="+name, nil)
	if err != nil {
		return nil, err
	}
	r, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(r.Body)
		return nil, fmt.Errorf("denials: status %d: %s", r.StatusCode, strings.TrimSpace(string(msg)))
	}
	var out []Denial
	if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// VMStatus queries the daemon for the project VM's current state,
// including the VM's IP address when it is running.
func (c *Client) VMStatus(ctx context.Context, name string) (VMStatusResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://localhost/vm/status?name="+name, nil)
	if err != nil {
		return VMStatusResponse{}, err
	}
	r, err := c.httpClient.Do(req)
	if err != nil {
		return VMStatusResponse{}, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return VMStatusResponse{}, fmt.Errorf("vm/status: status %d", r.StatusCode)
	}
	var resp VMStatusResponse
	if err := json.NewDecoder(r.Body).Decode(&resp); err != nil {
		return VMStatusResponse{}, err
	}
	return resp, nil
}

// post sends a JSON-body POST to the given path on the daemon socket.
func (c *Client) post(ctx context.Context, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", "http://localhost"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.httpClient.Do(req)
}
