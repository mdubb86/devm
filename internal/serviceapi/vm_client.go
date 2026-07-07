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

// ApplyEgressEnforcement asks the daemon to inject the iron-proxy
// nftables + dnsmasq scripts inside the VM. Called AFTER provisioning
// succeeds — the CLI runs the user's install: / apt-get / template
// installs with open network, then flips enforcement on just before
// systemd services start.
func (c *Client) ApplyEgressEnforcement(ctx context.Context, projectID, vmName string) error {
	body, err := json.Marshal(VMApplyEgressRequest{ProjectID: projectID, VMName: vmName})
	if err != nil {
		return err
	}
	r, err := c.post(ctx, "/vm/apply-egress-enforcement", body)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusNoContent {
		return fmt.Errorf("vm/apply-egress-enforcement: status %d", r.StatusCode)
	}
	return nil
}

// StopVM asks the daemon to stop the project VM. When vmName is set, the
// daemon calls `tart stop <vmName>` first so the guest gets a graceful
// shutdown before the tart-run process is signalled.
func (c *Client) StopVM(ctx context.Context, projectID, vmName string) error {
	body, err := json.Marshal(VMStopRequest{ProjectID: projectID, VMName: vmName})
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

// VMStatus queries the daemon for the project VM's current state.
// vmName is optional; when non-empty the daemon will attempt to
// surface the VM's IP address (only available when the VM is running).
func (c *Client) VMStatus(ctx context.Context, projectID, vmName string) (VMStatusResponse, error) {
	path := "/vm/status?project_id=" + projectID
	if vmName != "" {
		path += "&vm_name=" + vmName
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "http://localhost"+path, nil)
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
