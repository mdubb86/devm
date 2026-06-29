package serviceapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mdubb86/devm/internal/mac"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/supervisor"
)

// SecretBinding is one resolved, host-scoped secret. The CLI resolves
// Value from the login keychain in the user's context (the daemon runs as
// a LaunchDaemon and cannot) and sends it over the unix socket. Hosts is
// the injection scope from network.allow.
type SecretBinding struct {
	Name  string   `json:"name"`
	Value string   `json:"value"`
	Hosts []string `json:"hosts,omitempty"`
}

// VMStartRequest is the body shape for POST /vm/start.
type VMStartRequest struct {
	ProjectID         string          `json:"project_id"`
	VMName            string          `json:"vm_name"`
	WorkspaceHostPath string          `json:"workspace_host_path"`
	AllowList         []string        `json:"allow_list,omitempty"`
	Secrets           []SecretBinding `json:"secrets,omitempty"`
}

// VMStopRequest is the body shape for POST /vm/stop.
type VMStopRequest struct {
	ProjectID string `json:"project_id"`
}

// VMStatusResponse is the body shape for GET /vm/status.
type VMStatusResponse struct {
	Present bool   `json:"present"`
	Running bool   `json:"running"`
	PID     int    `json:"pid"`
	IP      string `json:"ip,omitempty"`
}

// RegisterVMHandlers wires /vm/start, /vm/stop, and /vm/status onto the
// given server. sup manages the VM process lifecycle; tr wraps the tart
// binary for clone, list, run, and IP queries.
func RegisterVMHandlers(s *Server, sup *supervisor.Supervisor, tr *tart.Tart) {
	s.Register("/vm/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req VMStartRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("bad json: %v", err), http.StatusBadRequest)
			return
		}
		if req.ProjectID == "" || req.VMName == "" {
			http.Error(w, "project_id and vm_name required", http.StatusBadRequest)
			return
		}

		ctx := r.Context()

		// Clone if absent.
		vms, err := tr.List(ctx)
		if err != nil {
			http.Error(w, fmt.Sprintf("tart list: %v", err), http.StatusInternalServerError)
			return
		}
		exists := false
		for _, vm := range vms {
			if vm.Name == req.VMName {
				exists = true
				break
			}
		}
		if !exists {
			if err := tr.Clone(ctx, "devm-base", req.VMName); err != nil {
				http.Error(w, fmt.Sprintf("tart clone: %v", err), http.StatusInternalServerError)
				return
			}
		}

		// Run options: net-shared, no graphics, workspace mount.
		opts := tart.RunOpts{
			NoGraphics: true,
		}
		if req.WorkspaceHostPath != "" {
			opts.DirMounts = []tart.DirMount{
				{
					Name:     "workspace",
					HostPath: req.WorkspaceHostPath,
					Tag:      "workspace",
				},
			}
		}
		cmd, err := tr.Run(ctx, req.VMName, opts)
		if err != nil {
			http.Error(w, fmt.Sprintf("tart run prep: %v", err), http.StatusInternalServerError)
			return
		}

		key := supervisor.Key{ProjectID: req.ProjectID, Role: supervisor.RoleVM}
		if err := sup.Spawn(ctx, key, cmd); err != nil {
			http.Error(w, fmt.Sprintf("supervisor spawn: %v", err), http.StatusInternalServerError)
			return
		}

		// Secrets are resolved CLI-side (login-keychain access); the CLI
		// sent us resolved values + host scopes directly.
		ironSecrets := make([]IronSecret, 0, len(req.Secrets))
		for _, b := range req.Secrets {
			ironSecrets = append(ironSecrets, IronSecret{Name: b.Name, Value: b.Value, Hosts: b.Hosts})
		}

		// Discover MAC_HOST (vmnet bridge IP).
		macIP, err := mac.Host()
		if err != nil {
			http.Error(w, fmt.Sprintf("discover MAC_HOST: %v", err), http.StatusInternalServerError)
			return
		}

		// Allocate three ephemeral ports on the Mac (HTTP + HTTPS + DNS).
		httpPort, err := pickPort()
		if err != nil {
			http.Error(w, fmt.Sprintf("pick http port: %v", err), http.StatusInternalServerError)
			return
		}
		httpsPort, err := pickPort()
		if err != nil {
			http.Error(w, fmt.Sprintf("pick https port: %v", err), http.StatusInternalServerError)
			return
		}
		dnsPort, err := pickPort()
		if err != nil {
			http.Error(w, fmt.Sprintf("pick dns port: %v", err), http.StatusInternalServerError)
			return
		}

		// Build iron-proxy config + spawn.
		caDir, err := EnsureRuntimeDir()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		proxyCfg := IronProxyConfig{
			HTTPListen:  fmt.Sprintf("%s:%d", macIP, httpPort),
			HTTPSListen: fmt.Sprintf("%s:%d", macIP, httpsPort),
			DNSListen:   fmt.Sprintf("%s:%d", macIP, dnsPort),
			CACertPath:  filepath.Join(caDir, "ca", "root.crt"),
			CAKeyPath:   filepath.Join(caDir, "ca", "root.key"),
			AllowList:   req.AllowList,
			Secrets:     ironSecrets,
		}
		if err := SpawnIronProxy(r.Context(), sup, req.ProjectID, proxyCfg); err != nil {
			http.Error(w, fmt.Sprintf("spawn iron-proxy: %v", err), http.StatusInternalServerError)
			return
		}

		// Stash port info for VM env injection to read.
		ironProxyState.put(req.ProjectID, ironProxyInfo{
			HTTPPort:  httpPort,
			HTTPSPort: httpsPort,
			DNSPort:   dnsPort,
		})

		// Apply VM-side config via tart exec — env, nftables, dnsmasq.
		// Three sudo-wrapped scripts run inside the VM. Each is its
		// own tart exec invocation; any failure rolls back nothing
		// (VM is in an indeterminate state — user re-runs devm
		// teardown to clean up).
		info, _ := ironProxyState.get(req.ProjectID)
		scripts := []string{
			buildEnvScript(),
			buildNftablesScript(macIP, info.HTTPPort, info.HTTPSPort, info.DNSPort),
			buildDnsmasqScript(macIP, info.DNSPort),
		}
		for i, script := range scripts {
			cmd := exec.Command("tart", "exec", "-i", req.VMName, "sudo", "bash", "-s")
			cmd.Stdin = strings.NewReader(script)
			out, err := cmd.CombinedOutput()
			if err != nil {
				http.Error(w, fmt.Sprintf("vm inject step %d failed: %v\n%s", i, err, out), http.StatusInternalServerError)
				return
			}
		}

		w.WriteHeader(http.StatusNoContent)
	})

	s.Register("/vm/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req VMStopRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("bad json: %v", err), http.StatusBadRequest)
			return
		}
		if req.ProjectID == "" {
			http.Error(w, "project_id required", http.StatusBadRequest)
			return
		}
		// Stop iron-proxy for this project first. Best-effort — if
		// it's not running, supervisor.Stop returns ErrNotFound which
		// we treat as success.
		key := supervisor.Key{ProjectID: req.ProjectID, Role: supervisor.RoleProxy}
		if err := sup.Stop(r.Context(), key); err != nil && !errors.Is(err, supervisor.ErrNotFound) {
			http.Error(w, fmt.Sprintf("stop iron-proxy: %v", err), http.StatusInternalServerError)
			return
		}
		ironProxyState.del(req.ProjectID)

		key = supervisor.Key{ProjectID: req.ProjectID, Role: supervisor.RoleVM}
		if err := sup.Stop(r.Context(), key); err != nil && !errors.Is(err, supervisor.ErrNotFound) {
			http.Error(w, fmt.Sprintf("supervisor stop: %v", err), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	s.Register("/vm/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		projectID := r.URL.Query().Get("project_id")
		if projectID == "" {
			http.Error(w, "project_id query param required", http.StatusBadRequest)
			return
		}
		key := supervisor.Key{ProjectID: projectID, Role: supervisor.RoleVM}
		state := sup.Status(key)

		resp := VMStatusResponse{
			Present: state.Present,
			Running: state.Running,
			PID:     state.PID,
		}

		// Fall back to tart's view if the supervisor doesn't know
		// about this VM. The supervisor's registry is in-memory and
		// lost across daemon restarts; tart is the authoritative
		// source for "does this VM exist / is it running". A running
		// VM tart owns can still be attached via `tart exec`.
		vmName := r.URL.Query().Get("vm_name")
		if vmName != "" && !state.Running {
			if vms, err := tr.List(r.Context()); err == nil {
				for _, vm := range vms {
					if vm.Name == vmName {
						resp.Present = true
						resp.Running = vm.Running
						break
					}
				}
			}
		}

		if vmName != "" && resp.Running {
			ip, _ := tr.IP(r.Context(), vmName)
			resp.IP = ip
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
}

// pickPort returns a free ephemeral TCP port by binding to :0 and
// immediately closing. There is a small TOCTOU window between the
// close and the caller's bind, but this is the standard approach
// on darwin where SO_REUSEPORT can't be used across processes.
func pickPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

type ironProxyInfo struct {
	HTTPPort  int
	HTTPSPort int
	DNSPort   int
}

type ironProxyStore struct {
	mu sync.Mutex
	m  map[string]ironProxyInfo
}

func newIronProxyStore() *ironProxyStore {
	return &ironProxyStore{m: make(map[string]ironProxyInfo)}
}

func (s *ironProxyStore) put(projectID string, info ironProxyInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[projectID] = info
}

func (s *ironProxyStore) get(projectID string) (ironProxyInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[projectID]
	return v, ok
}

func (s *ironProxyStore) del(projectID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, projectID)
}

var ironProxyState = newIronProxyStore()
