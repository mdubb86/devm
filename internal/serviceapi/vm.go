package serviceapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
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
	Name              string          `json:"name"`
	WorkspaceHostPath string          `json:"workspace_host_path"`
	AllowList         []string        `json:"allow_list,omitempty"`
	Secrets           []SecretBinding `json:"secrets,omitempty"`
	// ExtraMounts are additional host paths to share into the VM at the
	// same absolute path (mirrored). Each entry is the CLI-resolved form
	// `ABS_HOST_PATH[:ro]` (see schema.ResolveMount).
	ExtraMounts []string `json:"extra_mounts,omitempty"`
	// DiskSizeGB, when > 0, grows this VM's virtual disk to the given
	// gigabytes at clone time (a per-project `disk:` override). Zero
	// means the base image default. See schema.Config.DiskSizeGB.
	DiskSizeGB int `json:"disk_size_gb,omitempty"`
	// Docker mirrors cfg.Docker, stashed into ironProxyInfo so
	// /vm/apply-iron-proxy's re-hydration path can recover it without
	// re-reading the project's schema.Config.
	Docker bool `json:"docker,omitempty"`
}

// VMStopRequest is the body shape for POST /vm/stop. The daemon calls
// `tart stop <Name>` for a graceful guest shutdown before SIGTERM'ing the
// supervised tart process.
type VMStopRequest struct {
	Name string `json:"name"`
}

// VMEnforcementConfigResponse is the body shape for GET
// /vm/enforcement-config: everything the boot-integrity-gate composed
// provisioning script still bakes into its enforce phase. Egress
// allow-listing and DNS resolution are enforced by softnet over the
// control socket (POST /vm/apply-egress-enforcement), not by guest-side
// nftables/dnsmasq, so NftRuleset and DnsmasqScript are always empty.
// TimesyncdScript still points the guest's systemd-timesyncd at the proxy
// sentinel — softnet's UDP forwarder catches outbound udp:123 regardless
// of destination, but the guest must still be configured to send NTP
// somewhere for that interception to matter.
type VMEnforcementConfigResponse struct {
	NftRuleset      string `json:"nft_ruleset"`
	DnsmasqScript   string `json:"dnsmasq_script"`
	TimesyncdScript string `json:"timesyncd_script"`
}

// VMApplyEgressEnforcementRequest is the body shape for POST
// /vm/apply-egress-enforcement.
type VMApplyEgressEnforcementRequest struct {
	Name string `json:"name"`
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

// vmStopper is the subset of *tart.Tart that gracefulStopVM needs.
type vmStopper interface {
	Exec(ctx context.Context, name string, argv []string) tart.ExecResult
	List(ctx context.Context) ([]tart.VM, error)
}

// shutdownGraceTimeout bounds how long a stop waits for the guest to power
// itself off before the caller force-terminates it.
const shutdownGraceTimeout = 45 * time.Second

// gracefulStopVM asks the guest to power itself off cleanly and waits for
// the VM to leave the running state. `tart stop` crashes the guest instead
// of shutting it down (cirruslabs/tart#582, #659), which leaves docker
// `--restart` containers stuck "created" on the next boot; an in-guest
// `systemctl poweroff` lets systemd stop services cleanly so docker records
// them as running-on-boot. Best-effort: on timeout the caller's supervisor
// stop force-terminates the VM.
func gracefulStopVM(ctx context.Context, tr vmStopper, name string) {
	ctx, cancel := context.WithTimeout(ctx, shutdownGraceTimeout)
	defer cancel()

	// systemctl queues the shutdown and returns; the guest-agent connection
	// then drops as the VM goes down, so ignore the exec result. Bound it so
	// a hung agent can't consume the whole grace window.
	execCtx, execCancel := context.WithTimeout(ctx, 10*time.Second)
	_ = tr.Exec(execCtx, name, []string{"sudo", "systemctl", "poweroff"})
	execCancel()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if vms, err := tr.List(ctx); err == nil && !vmRunning(vms, name) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// vmRunning reports whether the named VM appears running in a `tart list`.
func vmRunning(vms []tart.VM, name string) bool {
	for _, v := range vms {
		if v.Name == name {
			return v.Running
		}
	}
	return false
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
// tests that don't spin up an NTP responder). locks serializes
// concurrent state-mutating calls for the same project; every handler
// registered here that mutates VM/proxy state acquires it on entry.
func RegisterVMHandlers(s *Server, sup *supervisor.Supervisor, tr *tart.Tart, denials *Denials, ntpPort int, locks *ProjectLocks) {
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
		if req.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}

		unlock := locks.Lock(req.Name)
		defer unlock()

		ctx := r.Context()

		// Clone if absent.
		vms, err := tr.List(ctx)
		if err != nil {
			http.Error(w, fmt.Sprintf("tart list: %v", err), http.StatusInternalServerError)
			return
		}
		exists := false
		for _, vm := range vms {
			if vm.Name == req.Name {
				exists = true
				break
			}
		}
		if !exists {
			if err := tr.Clone(ctx, "devm-base", req.Name); err != nil {
				http.Error(w, fmt.Sprintf("tart clone: %v", err), http.StatusInternalServerError)
				return
			}
			// Apply a per-project disk override while the freshly-cloned
			// VM is still stopped (tart set --disk-size requires a stopped
			// VM). Grow-only and floor-validated in schema, so this is
			// never a shrink; equal size is a safe no-op. The guest
			// filesystem is grown after boot via the growpart inject below.
			if req.DiskSizeGB > 0 {
				if err := tr.SetDiskSize(ctx, req.Name, req.DiskSizeGB); err != nil {
					http.Error(w, fmt.Sprintf("tart set --disk-size: %v", err), http.StatusInternalServerError)
					return
				}
			}
		}

		// Run options: softnet NIC, no graphics, workspace mount. softnet is
		// the daemon's sole egress path for every VM it launches.
		opts := tart.RunOpts{
			NoGraphics: true,
			NetSoftnet: true,
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
		cmd, err := tr.Run(ctx, req.Name, opts)
		if err != nil {
			http.Error(w, fmt.Sprintf("tart run prep: %v", err), http.StatusInternalServerError)
			return
		}

		// softnet is a symlink to this same devm binary; tart run
		// --net-softnet resolves a binary literally named "softnet" on the
		// child's $PATH and dispatches to softnet mode via argv[0].
		// pexec builds the child env solely from cmd.Env (no implicit
		// parent inheritance), so PATH and the control-socket location
		// must be set here explicitly, starting from a full os.Environ().
		binDir, err := ensureSoftnetSymlink()
		if err != nil {
			http.Error(w, fmt.Sprintf("ensure softnet symlink: %v", err), http.StatusInternalServerError)
			return
		}
		sock := SoftnetControlSock(req.Name)
		env := os.Environ()
		env = prependPathEnv(env, binDir)
		env = append(env, "SOFTNET_CONTROL_SOCK="+sock)
		cmd.Env = env
		softnetState.put(req.Name, sock)

		key := supervisor.Key{ProjectID: req.Name, Role: supervisor.RoleVM}
		if err := sup.Spawn(ctx, key, cmd); err != nil {
			http.Error(w, fmt.Sprintf("supervisor spawn: %v", err), http.StatusInternalServerError)
			return
		}

		// Wait for the Tart Guest Agent to come up before injecting
		// scripts via `tart exec`. Fresh VMs take a few seconds for
		// the agent to register; without this wait, the env script
		// (the first inject step) fires before the agent's gRPC
		// listener is reachable and the handler 500s.
		if err := waitVMExecReady(ctx, req.Name, 60*time.Second); err != nil {
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
		vmIP, err := tr.IP(ctx, req.Name)
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
			HTTPListen:  ironProxyListenAddr(httpPort),
			HTTPSListen: ironProxyListenAddr(httpsPort),
			DNSListen:   ironProxyListenAddr(dnsPort),
			// DNS answers with a sentinel IP (RFC 5737 documentation range,
			// never a real destination) so the guest's nftables DNAT rules
			// can catch the packet by port and rewrite to iron-proxy's real
			// address. If we returned macIP here, the guest's `ip daddr
			// <macIP> return` bypass would fire before DNAT and the packet
			// would connect to nothing on macIP:443.
			DNSProxyIP: proxySentinelIP,
			CACertPath: filepath.Join(caDir, "ca", "root.crt"),
			CAKeyPath:  filepath.Join(caDir, "ca", "root.key"),
			AllowList:  req.AllowList,
			Secrets:    ironSecrets,
		}
		if err := SpawnIronProxy(r.Context(), sup, req.Name, proxyCfg, denials); err != nil {
			http.Error(w, fmt.Sprintf("spawn iron-proxy: %v", err), http.StatusInternalServerError)
			return
		}

		// Stash port info + macIP for VM env injection and the deferred
		// egress-enforcement inject to read.
		ironProxyState.put(req.Name, ironProxyInfo{
			MacHost:   macIP,
			VMIP:      vmIP,
			HTTPPort:  httpPort,
			HTTPSPort: httpsPort,
			DNSPort:   dnsPort,
			Docker:    req.Docker,
		})

		// Apply VM-side config via tart exec — workspace mount, extra
		// mounts, env only. The iron-proxy egress-enforcement config
		// (nftables + dnsmasq→iron-proxy + timesyncd) is fetched by the
		// CLI orchestrator via GET /vm/enforcement-config and baked into
		// the composed provisioning script's enforce phase, so the
		// user's install:, apt-get, and template-install steps still run
		// with open egress — iron-proxy is meant to gate the
		// workload/services, not the developer's provisioning phase.
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
		// On a freshly-cloned VM that got a disk override, grow the guest
		// filesystem first so subsequent steps see the full disk.
		if !exists && req.DiskSizeGB > 0 {
			scripts = append([]string{buildGrowRootScript()}, scripts...)
		}
		for i, script := range scripts {
			cmd := exec.Command("tart", "exec", "-i", req.Name, "sudo", "bash", "-s")
			cmd.Stdin = strings.NewReader(script)
			out, err := cmd.CombinedOutput()
			if err != nil {
				http.Error(w, fmt.Sprintf("vm inject step %d failed: %v\n%s", i, err, out), http.StatusInternalServerError)
				return
			}
		}

		w.WriteHeader(http.StatusNoContent)
	})

	// /vm/enforcement-config returns the guest-side config the boot-
	// integrity-gate composed provisioning script still bakes into its
	// enforce phase. Egress allow-listing and DNS are enforced by softnet
	// over the control socket now (POST /vm/apply-egress-enforcement), so
	// only TimesyncdScript is populated.
	s.Register("/vm/enforcement-config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		if _, ok := ironProxyState.get(name); !ok {
			http.Error(w, "iron-proxy state missing — was /vm/start called for this project?",
				http.StatusPreconditionFailed)
			return
		}
		writeJSON(w, VMEnforcementConfigResponse{
			TimesyncdScript: buildTimesyncdScript(),
		})
	})

	// /vm/apply-egress-enforcement flips a project's softnet control
	// socket to ENFORCED, pointing egress at iron-proxy's loopback
	// endpoint and the daemon's SNTP responder. This is the sole egress
	// gate under softnet — there is no guest-side nftables/dnsmasq step
	// left to run here.
	s.Register("/vm/apply-egress-enforcement", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req VMApplyEgressEnforcementRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("bad json: %v", err), http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}

		unlock := locks.Lock(req.Name)
		defer unlock()

		info, ok := ironProxyState.get(req.Name)
		if !ok {
			http.Error(w, "iron-proxy state missing — was /vm/start called for this project?",
				http.StatusPreconditionFailed)
			return
		}

		// timesyncd still needs to run in-guest: softnet's UDP forwarder
		// catches outbound udp:123 regardless of destination, but the
		// guest itself must be configured to send NTP for that to matter.
		cmd := exec.Command("tart", "exec", "-i", req.Name, "sudo", "bash", "-s")
		cmd.Stdin = strings.NewReader(buildTimesyncdScript())
		if out, err := cmd.CombinedOutput(); err != nil {
			http.Error(w, fmt.Sprintf("apply timesyncd: %v\n%s", err, out), http.StatusInternalServerError)
			return
		}

		if err := sendSoftnetEnforced(softnetState.get(req.Name), info, ntpPort); err != nil {
			http.Error(w, fmt.Sprintf("flip softnet enforced: %v", err), http.StatusInternalServerError)
			return
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
		if req.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}

		unlock := locks.Lock(req.Name)
		defer unlock()

		// Stop iron-proxy for this project first. Best-effort — if
		// it's not running, supervisor.Stop returns ErrNotFound which
		// we treat as success.
		key := supervisor.Key{ProjectID: req.Name, Role: supervisor.RoleProxy}
		if err := sup.Stop(r.Context(), key); err != nil && !errors.Is(err, supervisor.ErrNotFound) {
			http.Error(w, fmt.Sprintf("stop iron-proxy: %v", err), http.StatusInternalServerError)
			return
		}
		ironProxyState.del(req.Name)
		if denials != nil {
			denials.Reset(req.Name)
		}

		// Power the guest off from the inside before force-terminating it.
		// `tart stop` crashes the guest rather than shutting it down
		// (cirruslabs/tart#582, #659), so systemd never stops services and
		// docker leaves its `--restart` containers stuck "created" on the
		// next boot. An in-guest `systemctl poweroff` runs a clean shutdown —
		// services stop, disk writes flush, docker records containers as
		// running-on-boot. The supervisor stop below force-terminates as a
		// fallback if the guest doesn't power off within the grace window.
		if req.Name != "" {
			gracefulStopVM(r.Context(), tr, req.Name)
		}

		key = supervisor.Key{ProjectID: req.Name, Role: supervisor.RoleVM}
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
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, "name query param required", http.StatusBadRequest)
			return
		}
		key := supervisor.Key{ProjectID: name, Role: supervisor.RoleVM}
		state := sup.Status(key)

		resp := VMStatusResponse{
			Present: state.Present,
			Running: state.Running,
			PID:     state.PID,
		}

		// tart is the authoritative source for "does this VM exist / is it
		// running" — the supervisor's key is (project, role) only, so
		// adoption across daemon restarts can re-attach to a PID from a
		// previous project run whose VM name no longer matches. Cross-check
		// the supervisor's claim against tart's list and let tart win.
		resp.Present = false
		resp.Running = false
		if vms, err := tr.List(r.Context()); err == nil {
			for _, vm := range vms {
				if vm.Name == name {
					resp.Present = true
					resp.Running = vm.Running
					break
				}
			}
		}

		if resp.Running {
			ip, _ := tr.IP(r.Context(), name)
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
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, "name query param required", http.StatusBadRequest)
			return
		}
		var snap []Denial
		if denials != nil {
			snap = denials.Snapshot(name)
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

// prependPathEnv returns env with dir prepended to the existing PATH
// entry, or a new PATH entry appended if env has none. Used to put the
// softnet symlink's directory ahead of the tart child's normal $PATH so
// `tart run --net-softnet` resolves it before any other binary literally
// named "softnet". Mutates and returns env in place.
func prependPathEnv(env []string, dir string) []string {
	for i, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			env[i] = "PATH=" + dir + ":" + strings.TrimPrefix(kv, "PATH=")
			return env
		}
	}
	return append(env, "PATH="+dir)
}

// sendSoftnetEnforced flips a project's softnet control socket to
// ENFORCED, forwarding egress to iron-proxy's HTTP/HTTPS/DNS listeners and
// the daemon's SNTP responder. All four addresses are loopback: softnet
// dials iron-proxy and the NTP responder host-side, not through a vmnet
// bridge, so MacHost never enters the endpoint it sends.
func sendSoftnetEnforced(sock string, info ironProxyInfo, ntpPort int) error {
	ep := &Endpoint{
		HTTP:  ironProxyListenAddr(info.HTTPPort),
		HTTPS: ironProxyListenAddr(info.HTTPSPort),
		DNS:   ironProxyListenAddr(info.DNSPort),
		NTP:   ironProxyListenAddr(ntpPort),
	}
	return newSoftnetClient(sock).setPolicy("ENFORCED", ep)
}

type ironProxyInfo struct {
	MacHost   string
	VMIP      string // the guest's current DHCP IP (for direct-service DNS)
	HTTPPort  int
	HTTPSPort int
	DNSPort   int
	Docker    bool // cfg.Docker, preserved across /vm/apply-iron-proxy re-hydration
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

// vmIPForProject returns the current stashed VM IP for a project, if the
// VM has been started this daemon lifetime. Used by the DNS server to
// answer direct-service hostnames.
func vmIPForProject(project string) (string, bool) {
	info, ok := ironProxyState.get(project)
	if !ok || info.VMIP == "" {
		return "", false
	}
	return info.VMIP, true
}
