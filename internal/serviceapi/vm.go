package serviceapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

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

// proxySentinelIP is the address iron-proxy returns for every allow-listed
// hostname. Chosen from RFC 5737 "documentation" space so it can never
// collide with a real destination. The guest's default route sends it to
// MAC_HOST via vmnet, where nftables DNAT catches `tcp dport 443/80` and
// rewrites the packet to iron-proxy's actual listen address. Using MAC_HOST
// itself here would trip the guest's `ip daddr <MAC_HOST> return` bypass
// (a legit rule for guest→iron-proxy DNS traffic) and skip DNAT entirely.
const proxySentinelIP = "192.0.2.1"

// VMStartRequest is the body shape for POST /vm/start.
type VMStartRequest struct {
	ProjectID         string          `json:"project_id"`
	VMName            string          `json:"vm_name"`
	WorkspaceHostPath string          `json:"workspace_host_path"`
	AllowList         []string        `json:"allow_list,omitempty"`
	Secrets           []SecretBinding `json:"secrets,omitempty"`
	// ExtraMounts are additional host paths to share into the VM at the
	// same absolute path (mirrored). Each entry is the CLI-resolved form
	// `ABS_HOST_PATH[:ro]` (see schema.ResolveMount).
	ExtraMounts []string `json:"extra_mounts,omitempty"`
}

// VMStopRequest is the body shape for POST /vm/stop.
// VMName is optional; when set, the daemon calls `tart stop <VMName>` for a
// graceful guest shutdown before SIGTERM'ing the supervised tart process.
type VMStopRequest struct {
	ProjectID string `json:"project_id"`
	VMName    string `json:"vm_name,omitempty"`
}

// VMApplyEgressRequest is the body shape for POST /vm/apply-egress-enforcement.
// The daemon looks up the iron-proxy port info stashed at /vm/start time
// and runs the nftables + dnsmasq scripts inside the VM.
type VMApplyEgressRequest struct {
	ProjectID string `json:"project_id"`
	VMName    string `json:"vm_name"`
}

// VMStatusResponse is the body shape for GET /vm/status.
type VMStatusResponse struct {
	Present bool   `json:"present"`
	Running bool   `json:"running"`
	PID     int    `json:"pid"`
	IP      string `json:"ip,omitempty"`
}

// waitVMExecReady polls `tart exec <name> true` until exit 0 or timeout.
// The Tart Guest Agent inside the VM takes a few seconds to register a
// listener after `tart run`; until it does, `tart exec` returns the
// gRPC connection error documented at /vm/start.
//
// Each attempt is bounded by a per-attempt context timeout so a single
// hung `tart exec` (which can happen under system load when the guest
// agent socket is slow) doesn't consume the whole budget.
func waitVMExecReady(ctx context.Context, vmName string, timeout time.Duration) error {
	const perAttemptTimeout = 3 * time.Second
	deadline := time.Now().Add(timeout)
	attempt := 0
	for time.Now().Before(deadline) {
		attempt++
		attemptCtx, cancel := context.WithTimeout(ctx, perAttemptTimeout)
		probe := exec.CommandContext(attemptCtx, "tart", "exec", vmName, "true")
		err := probe.Run()
		cancel()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
	return fmt.Errorf(
		"timeout waiting for vm %s to become exec-ready (%d attempts over %s)",
		vmName, attempt, timeout,
	)
}

// RegisterVMHandlers wires /vm/start, /vm/stop, /vm/status, and
// /denials onto the given server. sup manages the VM process
// lifecycle; tr wraps the tart binary for clone, list, run, and IP
// queries. denials is the daemon-scoped tracker fed by the iron-proxy
// audit tap — may be nil in tests that don't exercise denial paths.
// ntpPort is the UDP port the daemon's SNTP responder is listening on;
// the guest's nftables script DNATs its outbound UDP:123 to
// MAC_HOST:ntpPort so systemd-timesyncd resyncs from the host clock
// after a Mac sleep. Zero disables the NTP DNAT rule (useful in unit
// tests that don't spin up an NTP responder).
func RegisterVMHandlers(s *Server, sup *supervisor.Supervisor, tr *tart.Tart, denials *Denials, ntpPort int) {
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
			// Deliberate: no Name. A named share (`--dir=workspace:PATH`)
			// puts host content at MIRROR_PATH/workspace inside the guest
			// and the guest cannot write to MIRROR_PATH itself. Dropping
			// Name yields `--dir=PATH:tag=workspace`, mounting host content
			// directly at the mirror path.
			opts.DirMounts = []tart.DirMount{
				{
					HostPath: req.WorkspaceHostPath,
					Tag:      "workspace",
				},
			}
		}
		// Extra user-declared mounts. Each entry is `HOST_PATH[:ro]`
		// (already resolved CLI-side); tag is `extra_N` so the guest-side
		// mount script can address each share independently.
		//
		// We deliberately DON'T pass ReadOnly through to tart's --dir.
		// Apple Virtualization's parser gets confused by
		// `--dir=<path>:ro:tag=X` (interprets the path segment as the
		// share name — slashes then reject as "file system sensitive
		// characters"). Enforcing read-only via the guest mount script
		// (`mount -o ro`) is equivalent for our use.
		extraMountSpecs := parseExtraMounts(req.ExtraMounts)
		for i, m := range extraMountSpecs {
			opts.DirMounts = append(opts.DirMounts, tart.DirMount{
				HostPath: m.hostPath,
				Tag:      fmt.Sprintf("extra_%d", i),
			})
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

		// Wait for the Tart Guest Agent to come up before injecting
		// scripts via `tart exec`. Fresh VMs take a few seconds for
		// the agent to register; without this wait, the env script
		// (the first inject step) fires before the agent's gRPC
		// listener is reachable and the handler 500s.
		if err := waitVMExecReady(ctx, req.VMName, 60*time.Second); err != nil {
			http.Error(w, fmt.Sprintf("wait for vm exec-ready: %v", err), http.StatusInternalServerError)
			return
		}

		// Secrets are resolved CLI-side (login-keychain access); the CLI
		// sent us resolved values + host scopes directly.
		ironSecrets := make([]IronSecret, 0, len(req.Secrets))
		for _, b := range req.Secrets {
			ironSecrets = append(ironSecrets, IronSecret{Name: b.Name, Value: b.Value, Hosts: b.Hosts})
		}

		// Discover MAC_HOST (vmnet bridge IP that THIS VM is routed through).
		// Apple Virtualization creates one bridge* interface per VM group; a
		// Mac running several tart VMs can have several bridges, each with
		// its own /24 subnet. We must bind iron-proxy on the bridge whose
		// subnet contains OUR guest — otherwise the guest's default route
		// can't reach iron-proxy at all (silent DNS + egress failure).
		//
		// The VM has an IP by now (waitVMExecReady already succeeded, which
		// implies both the vmnet handshake and the guest agent are up).
		vmIP, err := tr.IP(ctx, req.VMName)
		if err != nil {
			http.Error(w, fmt.Sprintf("discover VM ip: %v", err), http.StatusInternalServerError)
			return
		}
		macIP, err := mac.HostForVM(vmIP)
		if err != nil {
			http.Error(w, fmt.Sprintf("discover MAC_HOST for vm %s: %v", vmIP, err), http.StatusInternalServerError)
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
			// DNS answers with a sentinel IP (RFC 5737 documentation range,
			// never a real destination) so the guest's nftables DNAT rules
			// can catch the packet by port and rewrite to iron-proxy's real
			// address. If we returned macIP here, the guest's `ip daddr
			// <macIP> return` bypass would fire before DNAT and the packet
			// would connect to nothing on macIP:443.
			DNSProxyIP:  proxySentinelIP,
			CACertPath:  filepath.Join(caDir, "ca", "root.crt"),
			CAKeyPath:   filepath.Join(caDir, "ca", "root.key"),
			AllowList:   req.AllowList,
			Secrets:     ironSecrets,
		}
		if err := SpawnIronProxy(r.Context(), sup, req.ProjectID, proxyCfg, denials); err != nil {
			http.Error(w, fmt.Sprintf("spawn iron-proxy: %v", err), http.StatusInternalServerError)
			return
		}

		// Stash port info + macIP for VM env injection and the deferred
		// egress-enforcement inject to read.
		ironProxyState.put(req.ProjectID, ironProxyInfo{
			MacHost:   macIP,
			HTTPPort:  httpPort,
			HTTPSPort: httpsPort,
			DNSPort:   dnsPort,
		})

		// Apply VM-side config via tart exec — workspace mount, extra
		// mounts, env only. The iron-proxy egress-enforcement scripts
		// (nftables + dnsmasq→iron-proxy) are DEFERRED to the post-
		// provision `/vm/apply-egress-enforcement` call so the user's
		// install:, apt-get, and template-install steps run with open
		// egress. Iron-proxy is meant to gate the workload/services, not
		// the developer's provisioning phase.
		//
		// Workspace mount runs first so subsequent scripts can read files
		// from the workspace (e.g. .devm/.env).
		scripts := []string{
			buildEnvScript(),
		}
		// Extra mounts must land BEFORE the env script so scripts that
		// read files from an extra mount can find them. Order among
		// extras doesn't matter — each is independent.
		for i, m := range extraMountSpecs {
			scripts = append([]string{
				buildExtraMountScript(fmt.Sprintf("extra_%d", i), m.hostPath, m.readOnly),
			}, scripts...)
		}
		if req.WorkspaceHostPath != "" {
			scripts = append([]string{buildWorkspaceMountScript(req.WorkspaceHostPath)}, scripts...)
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

	// /vm/apply-egress-enforcement injects the iron-proxy nftables +
	// dnsmasq scripts inside the VM. Called by the CLI orchestrator AFTER
	// provisioning succeeds — so the user's install:, apt-get, template
	// installs, etc. all run with open egress. Iron-proxy's purpose is
	// to gate the workload/services, not the provisioning phase.
	//
	// Idempotent: safe to call on a VM where enforcement is already
	// applied (nftables load overwrites, dnsmasq restart is a no-op if
	// the config didn't change).
	s.Register("/vm/apply-egress-enforcement", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req VMApplyEgressRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("bad json: %v", err), http.StatusBadRequest)
			return
		}
		if req.ProjectID == "" || req.VMName == "" {
			http.Error(w, "project_id and vm_name required", http.StatusBadRequest)
			return
		}
		info, ok := ironProxyState.get(req.ProjectID)
		if !ok {
			http.Error(w, "iron-proxy state missing — was /vm/start called for this project?",
				http.StatusPreconditionFailed)
			return
		}
		scripts := []string{
			buildNftablesScript(info.MacHost, info.HTTPPort, info.HTTPSPort, info.DNSPort, ntpPort),
			buildDnsmasqScript(info.MacHost, info.DNSPort),
			buildTimesyncdScript(),
		}
		for i, script := range scripts {
			cmd := exec.Command("tart", "exec", "-i", req.VMName, "sudo", "bash", "-s")
			cmd.Stdin = strings.NewReader(script)
			out, err := cmd.CombinedOutput()
			if err != nil {
				http.Error(w, fmt.Sprintf("apply egress step %d failed: %v\n%s", i, err, out),
					http.StatusInternalServerError)
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
		if denials != nil {
			denials.Reset(req.ProjectID)
		}

		// Ask tart for a graceful guest shutdown before SIGTERM'ing the
		// tart-run process. Without this, in-flight guest disk writes
		// aren't flushed and files written just before stop are lost.
		// Best-effort: `tart stop` on an already-stopped VM errors out;
		// carry on regardless — the supervisor stop below is what
		// releases devm's process handle.
		if req.VMName != "" {
			stopCtx, stopCancel := context.WithTimeout(r.Context(), 15*time.Second)
			_ = tr.Stop(stopCtx, req.VMName)
			stopCancel()
		}

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

		// When vm_name is provided, tart is the authoritative source
		// for "does this VM exist / is it running" — the supervisor's
		// key is (project_id, role) only, so adoption across daemon
		// restarts can re-attach to a PID from a previous project run
		// whose VM name no longer matches the current request. Cross-
		// check the supervisor's claim against tart's list and let
		// tart win.
		vmName := r.URL.Query().Get("vm_name")
		if vmName != "" {
			resp.Present = false
			resp.Running = false
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

	// /denials — read-only view of iron-proxy allow-list rejects for a
	// project. Sorted by count desc. Empty array is a normal state (no
	// rejects yet, or the process just respawned). Requires the tracker
	// to be wired — if not, we still respond 200 with [] so the CLI has a
	// uniform shape regardless of daemon build.
	s.Register("/denials", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		projectID := r.URL.Query().Get("project_id")
		if projectID == "" {
			http.Error(w, "project_id query param required", http.StatusBadRequest)
			return
		}
		var snap []Denial
		if denials != nil {
			snap = denials.Snapshot(projectID)
		}
		if snap == nil {
			snap = []Denial{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snap)
	})
}

// pickPort returns a free ephemeral TCP port by binding to :0 on
// 0.0.0.0 (all interfaces) and immediately closing. There is a small
// TOCTOU window between the close and iron-proxy's bind — standard on
// darwin where SO_REUSEPORT can't be shared across processes.
//
// The listen address must be 0.0.0.0, not 127.0.0.1: iron-proxy binds
// on MAC_HOST (a vmnet bridge IP like 192.168.64.1), not loopback. A
// port free on 127.0.0.1 can be held by another process on
// 192.168.64.1 — orphan iron-proxies from prior test runs, most
// commonly. Binding on 0.0.0.0 means the kernel only hands back a
// port free across every interface, so the subsequent iron-proxy bind
// on MAC_HOST can't collide.
func pickPort() (int, error) {
	l, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

type ironProxyInfo struct {
	MacHost   string
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
