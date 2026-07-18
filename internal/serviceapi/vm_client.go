package serviceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// StartVM asks the daemon to clone (if absent) and start the project VM.
func (c *Client) StartVM(ctx context.Context, req VMStartRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	r, err := c.post(ctx, "/vm/start", body)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(r.Body)
		return fmt.Errorf("vm/start: status %d: %s", r.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// EnforcementConfig asks the daemon for everything the boot-integrity-
// gate composed provisioning script bakes into its enforce phase — the
// enforced-egress nft ruleset, the dnsmasq upstream config, and the
// timesyncd NTP config — computed from the iron-proxy MAC_HOST/ports
// stashed at /vm/start. The orchestrator applies all three inside the
// single composed script's enforce-phase.
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

// OpenEgress calls POST /vm/open-egress, flipping the project's softnet
// control socket to OPEN. The orchestrator calls this immediately before
// running the composed provisioning script — softnet boots LOCKED, so
// without this call apt/install:/templates/startup: would run with no
// egress at all.
func (c *Client) OpenEgress(ctx context.Context, name string) error {
	body, err := json.Marshal(VMApplyEgressEnforcementRequest{Name: name})
	if err != nil {
		return err
	}
	r, err := c.post(ctx, "/vm/open-egress", body)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(r.Body)
		return fmt.Errorf("vm/open-egress: status %d: %s", r.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// ApplyEgressEnforcement calls POST /vm/apply-egress-enforcement, flipping
// the project's softnet control socket to ENFORCED. The orchestrator calls
// this immediately after the composed provisioning script succeeds, so
// egress is locked down to the real allowlist before the shell attaches.
func (c *Client) ApplyEgressEnforcement(ctx context.Context, name string) error {
	body, err := json.Marshal(VMApplyEgressEnforcementRequest{Name: name})
	if err != nil {
		return err
	}
	r, err := c.post(ctx, "/vm/apply-egress-enforcement", body)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(r.Body)
		return fmt.Errorf("vm/apply-egress-enforcement: status %d: %s", r.StatusCode, strings.TrimSpace(string(msg)))
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
