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

	"github.com/mdubb86/devm/internal/caenv"
	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/mutagen"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/softnet"
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

// interceptedEgressIP is the address the guest sees for every allow-listed
// egress destination — iron-proxy answers DNS with it; timesyncd sends
// NTP to it. Chosen from RFC 5737 "documentation" space so it can never
// collide with a real destination. Under ENFORCED policy, softnet forwards
// outbound TCP:80/443/UDP:123 to iron-proxy / the daemon's SNTP responder
// purely by destination port, regardless of destination IP, so traffic
// addressed to this address reaches the right service.
const interceptedEgressIP = "192.0.2.1"

// VMStartRequest is the body shape for POST /vm/start.
type VMStartRequest struct {
	Name string `json:"name"`
	// MacCwd is the project's Mac-side working directory absolute path.
	MacCwd    string          `json:"mac_cwd"`
	AllowList []string        `json:"allow_list,omitempty"`
	Secrets   []SecretBinding `json:"secrets,omitempty"`
	// DiskSizeGB, when > 0, grows this VM's virtual disk to the given
	// gigabytes at clone time (a per-project `disk:` override). Zero
	// means the base image default.
	DiskSizeGB int `json:"disk_size_gb,omitempty"`
	// MemoryMB, when > 0, sets the VM's memory to the given megabytes
	// via `tart set --memory` at start (a per-project `memory:`
	// override). Zero means the base image default.
	MemoryMB int `json:"memory_mb,omitempty"`
	// CpuCount, when > 0, sets the VM's CPU count via
	// `tart set --cpu` at start (a per-project `cpu:` override).
	// Zero means the base image default.
	CpuCount int `json:"cpu_count,omitempty"`
	// Cfg is the project's full config, used to compute the initial
	// softnet ingress expose map (see computeExposeMap) once the VM and
	// its control socket are up.
	Cfg schema.Config `json:"cfg"`
}

// VMStartResponse is the body shape for POST /vm/start.
type VMStartResponse struct {
	// ProjectIP is the project's allocated 127.42/16 loopback IP
	// (AllocateProjectIP), returned so the CLI can seed it into its
	// cold-start StateSnapshot. Without this, a daemon crash between
	// /vm/start and the first reconcile would leave ProjectIP unset in
	// the snapshot, and recoverProjectState would find nothing to
	// restore.
	ProjectIP string `json:"project_ip"`
	// TunnelPort is iron-proxy's CONNECT-capable tunnel_listen port —
	// the orchestrator needs it (with softnet.NATAliasIP) to build the
	// guest-visible HTTP_PROXY URL for its post-RunBundle, pre-RunUser
	// mutagen SetupReposPhase call, which the daemon has no visibility
	// into.
	TunnelPort int `json:"tunnel_port,omitempty"`
}

// VMStopRequest is the body shape for POST /vm/stop. The daemon calls
// `tart stop <Name>` for a graceful guest shutdown before SIGTERM'ing the
// supervised tart process.
type VMStopRequest struct {
	Name string `json:"name"`
}

// VMEgressPassthroughRequest is the body shape for POST
// /vm/passthrough-egress. DurationSeconds <= 0 lets the daemon apply
// defaultPassthroughSeconds — the CLI passes 0 when the user did not
// specify --for.
type VMEgressPassthroughRequest struct {
	Name            string `json:"name"`
	DurationSeconds int    `json:"duration_seconds,omitempty"`
}

// VMEgressPassthroughResponse is the response for POST
// /vm/passthrough-egress. WasOpen reports whether the project had an
// active passthrough window at the moment of the request — the CLI
// uses it to distinguish "opened fresh" from "renewed existing".
// ExpiresSeconds is the duration the daemon actually armed (the
// caller's value, or defaultPassthroughSeconds if the caller passed
// <= 0), suitable for a "auto-restores in Xs" user message.
type VMEgressPassthroughResponse struct {
	WasOpen        bool `json:"was_open"`
	ExpiresSeconds int  `json:"expires_seconds"`
}

// VMEgressRestrictResponse is the response for POST
// /vm/restrict-egress. WasOpen reports whether there was a window
// to restrict — false means it was already ENFORCED (or the project
// wasn't tracked), which the CLI surfaces as a "nothing to do"
// message rather than an error.
type VMEgressRestrictResponse struct {
	WasOpen bool `json:"was_open"`
}

// EgressStatus is the response for GET /vm/egress-status. Policy is
// "restricted" (the default ENFORCED posture) or "passthrough" (an
// active `devm passthrough` window). PassthroughExpiresAt is set
// only when a window is active — the deadline at which the daemon
// will auto-restore ENFORCED.
type EgressStatus struct {
	Policy               string     `json:"policy"`
	PassthroughExpiresAt *time.Time `json:"passthrough_expires_at,omitempty"`
}

// VMEnforcementConfigResponse is the body shape for GET
// /vm/enforcement-config. Egress allow-listing and DNS resolution are
// enforced by softnet over the control socket (POST
// /vm/end-provisioning), not by guest-side nftables/dnsmasq.
// timesyncd's NTP config used to be applied here at runtime
// (TimesyncdScript); it's now baked into the base image at
// image/provision-base.sh, since it's static — no per-project or
// per-install variation. The handler is kept as a precondition check
// (this project's iron-proxy state must exist) that callers can probe
// before provisioning proceeds.
type VMEnforcementConfigResponse struct{}

// VMApplyEgressEnforcementRequest is the body shape for POST
// /vm/begin-provisioning and /vm/end-provisioning.
type VMApplyEgressEnforcementRequest struct {
	Name string `json:"name"`
}

// VMVolumeSyncRequest is the body shape for POST /vm/volume-sync.
// Cfg carries the project's full config so the daemon can build the
// mutagen entity list (BuildEntities) itself — it has no other
// visibility into the project's volumes/repos configuration.
type VMVolumeSyncRequest struct {
	Name string `json:"name"`
	// RepoRoot is the project's Mac-side working directory absolute
	// path.
	RepoRoot string        `json:"repo_root"`
	Cfg      schema.Config `json:"cfg"`
}

// VMRepoCloneRequest is the body shape for POST /vm/repo-clone.
type VMRepoCloneRequest struct {
	Name     string        `json:"name"`
	RepoRoot string        `json:"repo_root"`
	Cfg      schema.Config `json:"cfg"`
	// TunnelPort is iron-proxy's CONNECT-capable tunnel_listen port —
	// combined with softnet.NATAliasIP it forms the guest-visible
	// HTTP_PROXY URL a guest-side git clone needs.
	TunnelPort int `json:"tunnel_port"`
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
// them as running-on-boot. Best-effort: on timeout the caller force-
// terminates the VM via the supervisor (see the sup.DisableRestart +
// sup.Stop calls in /vm/stop — callers MUST call sup.DisableRestart for
// the VM's tart-run process before calling this, or the natural process
// exit that a successful poweroff causes will be read by the supervisor's
// OnUnexpectedExit hook as an unexpected crash and respawned, defeating
// this function's poll and stalling it to the full timeout).
func gracefulStopVM(ctx context.Context, tr vmStopper, name string) {
	ctx, cancel := context.WithTimeout(ctx, shutdownGraceTimeout)
	defer cancel()

	// systemctl queues the shutdown and returns; the guest-agent connection
	// then drops as the VM goes down, so ignore the exec result. Bound it so
	// a hung agent can't consume the whole grace window.
	execCtx, execCancel := context.WithTimeout(ctx, 10*time.Second)
	_ = tr.Exec(execCtx, name, []string{"sudo", "systemctl", "poweroff"})
	execCancel()

	// Poll for the guest actually going down. Under --net-softnet, `tart
	// list`'s Running flag never reflects the in-guest poweroff (the tart
	// process itself outlives the guest's network state — the same
	// tart/softnet process-lifecycle gap this repo works around
	// elsewhere), so a List-only poll would spin the full
	// shutdownGraceTimeout on every stop. Instead probe guest-agent
	// reachability directly: `tart exec` rides the vsock guest-agent
	// channel, which is independent of the softnet NIC, so once the guest
	// actually halts the agent goes away and Exec starts failing. Require
	// 3 consecutive failures (1.5s at the 500ms poll interval) before
	// declaring the guest down: a single transient agent hiccup — or even
	// two, e.g. host contention while `systemctl poweroff` is mid-flush of
	// docker's storage layers — can otherwise read as "halted" and trigger
	// an ungraceful force-stop on a guest that's still very much alive,
	// which is exactly the outcome this whole probe exists to avoid.
	// Pathologically a longer stall could still misread as down; the 45s
	// cap plus the caller's supervisor force-stop remain the backstop for
	// that case. Still check List first on every tick and return
	// immediately on a reported stop — the fast path if tart list's
	// Running flag ever does track guest state (e.g. non-softnet NICs).
	const requiredConsecutiveFailures = 3
	consecutiveExecFailures := 0
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if vms, err := tr.List(ctx); err == nil && !vmRunning(vms, name) {
			return
		}

		probeCtx, probeCancel := context.WithTimeout(ctx, 3*time.Second)
		result := tr.Exec(probeCtx, name, []string{"true"})
		probeCancel()
		if result.ExitCode != 0 {
			consecutiveExecFailures++
			if consecutiveExecFailures >= requiredConsecutiveFailures {
				return
			}
		} else {
			consecutiveExecFailures = 0
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

// armPassthroughRestoreTimer schedules the policy authority to be
// restored to ModeRestricted after d, the bound
// `devm passthrough --for <dur>` (or the default) puts on a
// supervised window. Installing it via egressPassthroughState.setTimer
// stops+replaces whatever restore timer was already pending for name,
// so repeated passthroughs (or a restrict in between) never leave two
// timers racing.
//
// The callback runs on its own goroutine (time.AfterFunc, not
// inline), so taking locks.Lock(name) here is not nested under any
// handler's lock. It re-checks egressPassthroughState right before
// flipping the mode — by the time it fires, the project may have been
// stopped, torn down, or restricted early by `devm restrict`, all of
// which call del/stopTimer and so would have already cancelled this
// timer; the re-check is therefore belt-and-suspenders against the
// timer having fired the instant before a racing cancellation.
func armPassthroughRestoreTimer(locks *ProjectLocks, name string, d time.Duration) {
	// Forward-declare t so the closure captures the variable in scope;
	// value assigned by time.AfterFunc's return below. Callback checks
	// e.restore == t under the lock — pointer-identity check that
	// guarantees a stale callback (one whose Stop lost the race with
	// its own fire, replaced mid-AfterFunc by a newer setTimer) exits
	// before touching the authority.
	var t *time.Timer
	t = time.AfterFunc(d, func() {
		unlock := locks.Lock(name)
		defer unlock()

		e, ok := egressPassthroughState.get(name)
		if !ok || e.restore != t {
			return // restricted/stopped/torn down since open, or superseded by a newer timer
		}
		policyAuthority.SetMode(name, ModeRestricted)
		egressPassthroughState.del(name)
	})
	egressPassthroughState.setTimer(name, t)
}

// shutdownSoftnet asks projectID's softnet child to exit, over its control
// socket, if the daemon has one recorded. Best-effort and silent when there
// is nothing to shut down (projectID's VM was never started, or /vm/stop
// already ran for it) — softnetState.get returning "" is the normal case
// for a project whose softnet, if any, is already gone.
//
// This exists because softnet is not a process the daemon spawns/tracks
// directly: `tart run --net-softnet` forks it internally as its own child,
// so it's invisible to supervisor.Supervisor (which only knows about the
// `tart run` process itself, registered under supervisor.RoleVM). Stopping
// that process does not reliably stop softnet too — see the shutdown()
// doc comment in softnet_control.go for why — so without this call softnet
// outlives its owning VM as an orphan, still holding the project's bound
// 127.42.0.N port for the next cold-start to collide with.
func shutdownSoftnet(projectID string) {
	sock := softnetState.get(projectID)
	if sock == "" {
		return
	}
	if err := newSoftnetClient(sock).shutdown(); err != nil {
		daemonlog.Errorf("serviceapi: vm/stop: softnet shutdown for %s: %v", projectID, err)
	}
}

// RegisterVMHandlers wires /vm/start, /vm/stop, /vm/status, and
// /denials onto the given server. sup manages the VM process
// lifecycle; tr wraps the tart binary for clone, list, run, and IP
// queries. denials is the daemon-scoped tracker fed by the iron-proxy
// audit tap — may be nil in tests that don't exercise denial paths.
// ntpPort is the UDP port the daemon's SNTP responder is listening on;
// under ENFORCED policy, softnet forwards the guest's outbound UDP:123
// to this port so systemd-timesyncd resyncs from the host clock
// after a Mac sleep. Zero disables NTP forwarding (useful in unit
// tests that don't spin up an NTP responder). locks serializes
// concurrent state-mutating calls for the same project; every handler
// registered here that mutates VM/proxy state acquires it on entry.
// proxy is the daemon's HTTP/HTTPS reverse proxy; /vm/start binds its
// per-project listeners once the project IP is allocated, and /vm/stop
// tears them down. May be nil in tests that don't exercise the proxy
// lifecycle — StartProjectListeners/StopProjectListeners are skipped
// in that case.
func RegisterVMHandlers(s *Server, cfg identity.Config, sup *supervisor.Supervisor, tr *tart.Tart, denials *Denials, ntpPort int, locks *ProjectLocks, proxy *ProxyServer) {
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
			if err := tr.Clone(ctx, cfg.BaseImageName(), req.Name); err != nil {
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

		// Memory/CPU overrides apply on every start, not just clone: a
		// changed `memory:`/`cpu:` reconciles as BucketRestartVM (VM stop
		// + cold start, no reclone), so this VM may already exist and
		// just be stopped. tart set requires a stopped VM, which holds
		// here — /vm/start is only called against a non-running sandbox.
		//
		// Reject an override the host can't satisfy before handing it to
		// tart, so the failure is a friendly 400 instead of a lower-level
		// tart error surfacing later in the clone/start pipeline.
		if req.MemoryMB > 0 || req.CpuCount > 0 {
			memBytes, cpus, err := hostCapacity()
			if err != nil {
				daemonlog.Errorf("serviceapi: vm/start: host-capacity check skipped: %v", err)
			} else {
				hostMemMB := memBytes / (1024 * 1024)
				if req.MemoryMB > 0 && uint64(req.MemoryMB) > hostMemMB {
					http.Error(w, fmt.Sprintf("memory: %d MB exceeds host capacity (%d MB)", req.MemoryMB, hostMemMB), http.StatusBadRequest)
					return
				}
				if req.CpuCount > 0 && uint32(req.CpuCount) > cpus {
					http.Error(w, fmt.Sprintf("cpu: %d exceeds host count (%d)", req.CpuCount, cpus), http.StatusBadRequest)
					return
				}
			}
		}
		if req.MemoryMB > 0 {
			if err := tr.SetMemory(ctx, req.Name, req.MemoryMB); err != nil {
				http.Error(w, fmt.Sprintf("tart set --memory: %v", err), http.StatusInternalServerError)
				return
			}
		}
		if req.CpuCount > 0 {
			if err := tr.SetCPU(ctx, req.Name, req.CpuCount); err != nil {
				http.Error(w, fmt.Sprintf("tart set --cpu: %v", err), http.StatusInternalServerError)
				return
			}
		}

		// Run options: softnet NIC, no graphics. softnet is the daemon's
		// sole egress path for every VM it launches. Volumes, repos, and
		// extra mounts are no longer virtiofs shares — mutagen sync
		// sessions (SetupVolumesPhase, below) carry that traffic instead.
		opts := tart.RunOpts{
			NoGraphics: true,
			NetSoftnet: true,
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
		binDir, err := ensureSoftnetSymlink(cfg)
		if err != nil {
			http.Error(w, fmt.Sprintf("ensure softnet symlink: %v", err), http.StatusInternalServerError)
			return
		}
		if err := ensureSoftnetSockDir(softnetSockDir()); err != nil {
			http.Error(w, fmt.Sprintf("softnet sock dir: %v", err), http.StatusInternalServerError)
			return
		}
		sock := SoftnetControlSock(cfg, req.Name)
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

		// Allocate the per-project loopback IP (127.42.0.N). All ingress
		// listeners for this project — softnet's direct-service ports,
		// softnet SSH on :22, and this daemon's own per-project HTTP/HTTPS
		// proxy listeners — bind on this address. Idempotent: a project
		// that already has one (e.g. re-`devm shell` on an already-running
		// VM) keeps it.
		projectIP, err := AllocateProjectIP(cfg, req.Name)
		if err != nil {
			http.Error(w, fmt.Sprintf("allocate project ip: %v", err), http.StatusInternalServerError)
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

		// Push the initial ingress expose map. Independent of egress
		// state; listeners bind on the host and forward lazily once
		// guest services come up. Deliberately after waitVMExecReady:
		// softnet creates its control socket when `tart run` forks it
		// during network setup, but that can land a beat after this
		// handler reaches the push — softnetClient.dial's ~1s retry
		// window has lost that race in the field, leaving the project
		// with no :22 ingress (SSH dead) behind a successful start.
		// Exec-ready implies the guest agent is up, which softnet's
		// socket creation precedes by the whole guest boot. Fatal:
		// softnet acks the push with per-port bind results, and a
		// failed bind means the project's ingress — :22 included — is
		// partially dead behind what would otherwise report as a
		// successful start. Better a loud failed start naming the
		// port than a VM whose SSH endpoint silently belongs to
		// someone else.
		if err := pushExposeMap(req.Name, computeExposeMap(req.Cfg, projectIP)); err != nil {
			daemonlog.Errorf("serviceapi: vm/start: push expose map for %s: %v", req.Name, err)
			http.Error(w, fmt.Sprintf("push expose map: %v", err), http.StatusInternalServerError)
			return
		}
		if err := pushTestHosts(req.Name, computeDirectTestHosts(req.Cfg)); err != nil {
			daemonlog.Errorf("serviceapi: vm/start: push test hosts for %s: %v", req.Name, err)
		}

		// Secrets are resolved CLI-side (login-keychain access); the CLI
		// sent us resolved values + host scopes directly.
		ironSecrets := make([]IronSecret, 0, len(req.Secrets))
		for _, b := range req.Secrets {
			ironSecrets = append(ironSecrets, IronSecret{Name: b.Name, Value: b.Value, Hosts: b.Hosts})
		}

		// Allocate four ephemeral ports on the Mac
		// (HTTP + HTTPS + TUNNEL + DNS).
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
		tunnelPort, err := pickPort()
		if err != nil {
			http.Error(w, fmt.Sprintf("pick tunnel port: %v", err), http.StatusInternalServerError)
			return
		}
		dnsPort, err := pickPort()
		if err != nil {
			http.Error(w, fmt.Sprintf("pick dns port: %v", err), http.StatusInternalServerError)
			return
		}

		// Build iron-proxy config + spawn.
		caDir, err := EnsureRuntimeDir(cfg)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		proxyCfg := IronProxyConfig{
			HTTPListen:   ironProxyListenAddr(httpPort),
			HTTPSListen:  ironProxyListenAddr(httpsPort),
			TunnelListen: ironProxyListenAddr(tunnelPort),
			DNSListen:    ironProxyListenAddr(dnsPort),
			// DNS answers with interceptedEgressIP (RFC 5737 documentation
			// range, never a real destination); softnet forwards outbound
			// TCP:80/443 to iron-proxy purely by destination port under
			// ENFORCED policy, so traffic to that address reaches
			// iron-proxy the same as any other allow-listed destination.
			DNSProxyIP: interceptedEgressIP,
			CACertPath: filepath.Join(caDir, "ca", "root.crt"),
			CAKeyPath:  filepath.Join(caDir, "ca", "root.key"),
			AllowList:  req.AllowList,
			Secrets:    ironSecrets,
		}
		if err := SpawnIronProxy(r.Context(), cfg, sup, req.Name, proxyCfg, denials); err != nil {
			http.Error(w, fmt.Sprintf("spawn iron-proxy: %v", err), http.StatusInternalServerError)
			return
		}

		// Allocate a port and bind the per-project pop HTTP listener. The
		// forward from guest 192.168.127.1:81 → this address is wired via
		// ForwardTargets.Pop in endpointFrom below.
		popPort, err := pickPort()
		if err != nil {
			http.Error(w, fmt.Sprintf("pick pop port: %v", err), http.StatusInternalServerError)
			return
		}
		popLn, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", popPort))
		if err != nil {
			http.Error(w, fmt.Sprintf("bind pop listener: %v", err), http.StatusInternalServerError)
			return
		}
		// Register before spawning the serve goroutine — a fast /vm/stop
		// racing the goroutine's own startup could otherwise call
		// closePopListener before the listener is recorded, leaking the
		// fd. Mirrors ProxyServer.recordProjectListeners in proxy.go.
		popListeners.Store(req.Name, popLn)
		go servePopListener(popLn, cfg, req.Name)

		// Stash port info for VM env injection and the deferred
		// egress-enforcement inject to read. Merge onto the existing
		// entry rather than overwrite — AllocateProjectIP above already
		// stashed ProjectIP, and a raw put here would silently clobber it
		// back to empty.
		info, _ := ironProxyState.get(req.Name)
		info.HTTPPort = httpPort
		info.HTTPSPort = httpsPort
		info.TunnelPort = tunnelPort
		info.DNSPort = dnsPort
		info.PopPort = popPort
		ironProxyState.put(req.Name, info)

		// Apply VM-side config via tart exec. timesyncd's NTP config is
		// baked into the base image (image/provision-base.sh), not
		// applied here — the user's install:, apt-get, and
		// template-install steps still run with open egress; iron-proxy
		// is meant to gate the workload/services, not the developer's
		// provisioning phase.
		var scripts []string
		// On a freshly-cloned VM that got a disk override, grow the guest
		// filesystem first so subsequent steps see the full disk.
		if !exists && req.DiskSizeGB > 0 {
			scripts = append(scripts, buildGrowRootScript())
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

		// Bind this project's per-project HTTP/HTTPS proxy listeners on
		// its ProjectIP via the helper. Non-fatal like the
		// expose-map push above: ingress is convenience, not the security
		// boundary, and a failed bind (e.g. the helper isn't
		// installed) is re-attempted on the next /vm/start.
		if proxy != nil {
			if err := proxy.StartProjectListeners(ctx, req.Name, projectIP); err != nil {
				daemonlog.Errorf("serviceapi: vm/start: start project listeners for %s: %v", req.Name, err)
			}

			guestHTTPPort, guestHTTPSPort, err := proxy.StartGuestOriginListeners(ctx, req.Name, projectIP)
			if err != nil {
				http.Error(w, fmt.Sprintf("start guest-origin listeners: %v", err), http.StatusInternalServerError)
				return
			}
			info.GuestHTTPPort = guestHTTPPort
			info.GuestHTTPSPort = guestHTTPSPort
			ironProxyState.put(req.Name, info)
		}

		writeJSON(w, VMStartResponse{ProjectIP: projectIP, TunnelPort: info.TunnelPort})
	})

	// /vm/enforcement-config is a precondition check that this project's
	// iron-proxy state exists (the orchestrator calls it before
	// provisioning proceeds). It used to also return the guest-side
	// timesyncd config; that's now baked into the base image, so the
	// response body carries nothing.
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
		writeJSON(w, VMEnforcementConfigResponse{})
	})

	// /vm/begin-provisioning flips a project's softnet control socket to
	// ENFORCED-behavior (:80/:443 route to iron-proxy) and the egress
	// policy authority to ModePassthrough — the provisioning window (apt,
	// install:, templates, startup:) runs with iron-proxy already in the
	// traffic path, gated only by the authority's passthrough mode rather
	// than the real allowlist. Called post-RunBundle (the guest trust
	// store already has the iron-proxy CA) and pre-RunUser. softnet boots
	// LOCKED, so cold-start provisioning would otherwise run with no
	// egress at all.
	s.Register("/vm/begin-provisioning", func(w http.ResponseWriter, r *http.Request) {
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

		sock := softnetState.get(req.Name)
		if sock == "" {
			http.Error(w, "softnet control socket missing — was /vm/start called for this project?",
				http.StatusPreconditionFailed)
			return
		}

		policyAuthority.SetMode(req.Name, ModePassthrough)

		// Full ForwardTargets on every push — setPolicy keeps the previous
		// endpoint on nil, so a partial push would silently clobber fields.
		openInfo, _ := ironProxyState.get(req.Name)
		if err := newSoftnetClient(sock).setPolicy("ENFORCED", endpointFrom(openInfo, ntpPort)); err != nil {
			http.Error(w, fmt.Sprintf("flip softnet enforced: %v", err), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	})

	// /vm/volume-sync establishes a mutagen sync session for every entity
	// (volumes and repos alike) — the uniform half of workspace hydration.
	// Called after /vm/begin-provisioning and before /vm/repo-clone.
	s.Register("/vm/volume-sync", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req VMVolumeSyncRequest
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

		entities, err := BuildEntities(&req.Cfg, req.RepoRoot)
		if err != nil {
			http.Error(w, fmt.Sprintf("build entities: %v", err), http.StatusInternalServerError)
			return
		}

		mutagenBin, err := mutagenEnsureFn(cfg.RuntimeDir())
		if err != nil {
			http.Error(w, fmt.Sprintf("mutagen: extract binary: %v", err), http.StatusInternalServerError)
			return
		}
		mutagenCLI := &mutagen.CLI{Binary: mutagenBin, DataDir: mutagenDataDir(cfg)}

		guestSSHTarget := "devm-" + req.Name
		if err := SetupVolumesPhase(r.Context(), mutagenCLI, cfg, req.Name, entities,
			tartGuestExec(r.Context(), tr, req.Name), guestSSHTarget); err != nil {
			http.Error(w, fmt.Sprintf("volume sync: %v", err), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	})

	// /vm/repo-clone runs a cold-start git clone, through iron-proxy, for
	// every repo entity where the relevant sides are empty. Called after
	// /vm/volume-sync — the mutagen sessions it establishes pick up the
	// freshly-cloned guest content on their own.
	s.Register("/vm/repo-clone", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req VMRepoCloneRequest
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

		entities, err := BuildEntities(&req.Cfg, req.RepoRoot)
		if err != nil {
			http.Error(w, fmt.Sprintf("build entities: %v", err), http.StatusInternalServerError)
			return
		}

		ironProxyURL := fmt.Sprintf("http://%s:%d", softnet.NATAliasIP, req.TunnelPort)
		if err := SetupReposPhase(r.Context(), cfg, req.Name, entities,
			tartGuestExec(r.Context(), tr, req.Name), ironProxyURL, guestGitCACertPath()); err != nil {
			http.Error(w, fmt.Sprintf("repo clone: %v", err), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	})

	// /vm/end-provisioning flips the egress policy authority back to
	// ModeRestricted — the real allowlist governs from here on. Called
	// pre-RunEnforced. Softnet stays in ENFORCED-behavior (iron-proxy
	// stays in the traffic path); the authority mode is the only gate
	// this handler changes.
	s.Register("/vm/end-provisioning", func(w http.ResponseWriter, r *http.Request) {
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

		policyAuthority.SetMode(req.Name, ModeRestricted)
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

		defer egressPassthroughState.del(req.Name)

		// Flush + pause this project's mutagen sessions before anything
		// below touches the guest's network or power state — mutagen's
		// transport is tart exec (see cmd/tart-mutagen-ssh), which needs
		// the guest running and reachable, and gracefulStopVM's in-guest
		// poweroff (further down) ends that. Best-effort: mutagen's own
		// journal handles a crash mid-flush, so a failure here just means
		// sessions resume unflushed on the next start instead of failing
		// the stop.
		if err := mutagenStopPhaseFn(cfg, req.Name); err != nil {
			daemonlog.Errorf("mutagen stop phase for %s: %v (continuing)", req.Name, err)
		}

		// Stop iron-proxy for this project first. Best-effort — if
		// it's not running, supervisor.Stop returns ErrNotFound which
		// we treat as success.
		key := supervisor.Key{ProjectID: req.Name, Role: supervisor.RoleProxy}
		if err := sup.Stop(r.Context(), key); err != nil && !errors.Is(err, supervisor.ErrNotFound) {
			http.Error(w, fmt.Sprintf("stop iron-proxy: %v", err), http.StatusInternalServerError)
			return
		}
		// Close this project's per-project HTTP/HTTPS proxy listeners
		// before releasing its IP — the IP must not be handed to another
		// project's /vm/start while this project might still be
		// listening on it.
		if proxy != nil {
			proxy.StopProjectListeners(req.Name)
		}
		closePopListener(req.Name)
		policyAuthority.StopServing(req.Name)
		ReleaseProjectIP(cfg, req.Name)
		ironProxyState.del(req.Name)
		// A stopped project frees its claimed host ports for other
		// projects to take.
		exposeClaims.release(req.Name)
		if denials != nil {
			denials.Reset(req.Name)
		}

		// Disable the supervisor's auto-respawn for the VM's tart-run
		// process BEFORE asking the guest to power off. gracefulStopVM's
		// in-guest `systemctl poweroff` makes `tart run` exit on its own
		// in ~5s; without this gate, the supervisor's OnUnexpectedExit
		// callback reads that exit as unexpected and respawns tart run,
		// booting a new guest that gracefulStopVM's poll then dutifully
		// waits out to the full shutdownGraceTimeout ceiling. DisableRestart
		// flips a flag consulted by the still-armed callback — it does not
		// touch the registry, so the belt-and-suspenders sup.Stop below
		// still finds (and if needed force-kills) the entry.
		key = supervisor.Key{ProjectID: req.Name, Role: supervisor.RoleVM}
		sup.DisableRestart(key)

		// Power the guest off from the inside before force-terminating it.
		// `tart stop` crashes the guest rather than shutting it down
		// (cirruslabs/tart#582, #659), so systemd never stops services and
		// docker leaves its `--restart` containers stuck "created" on the
		// next boot. An in-guest `systemctl poweroff` runs a clean shutdown —
		// services stop, disk writes flush, docker records containers as
		// running-on-boot. The sup.Stop below is a fallback if the guest
		// doesn't power off within the grace window.
		if req.Name != "" {
			gracefulStopVM(r.Context(), tr, req.Name)
		}

		// Belt-and-suspenders: SIGTERM (then SIGKILL on timeout) whatever
		// is left of the tart-run process. If gracefulStopVM's poweroff
		// already made it exit, this is a fast no-op — pexec finds the
		// process already reaped and returns immediately. ErrNotFound
		// means /vm/start never ran (or a prior /vm/stop already reaped
		// this key), which is fine.
		if err := sup.Stop(r.Context(), key); err != nil && !errors.Is(err, supervisor.ErrNotFound) {
			daemonlog.Errorf("serviceapi: vm/stop: supervisor stop for %s: %v", req.Name, err)
		}
		// Ask softnet to exit now that the guest is confirmed stopped and
		// its tart-run process confirmed terminated above. softnet is a
		// child `tart run --net-softnet` forks internally, not a process
		// the supervisor spawns/tracks itself (see /vm/start), so stopping
		// the VM's tart-run process does not reliably reach it — see
		// shutdownSoftnet. Signalling it here, rather than before
		// gracefulStopVM, means the guest keeps its network intact through
		// its own in-guest poweroff sequence instead of losing it mid-
		// shutdown (which stalled network-dependent systemd units and
		// pushed gracefulStopVM toward its full grace-period ceiling).
		shutdownSoftnet(req.Name)
		softnetState.del(req.Name)
		w.WriteHeader(http.StatusNoContent)
	})

	// /vm/passthrough-egress opens a time-bounded egress passthrough
	// window: flips the policy authority to ModePassthrough for the
	// duration, arms a timer to restore ModeRestricted on expiry.
	// Repeat opens replace the existing timer. Reconcile does NOT
	// close the window; only the timer, `devm restrict`, or /vm/stop
	// do. Softnet and iron-proxy are untouched — the authority mode
	// is the entire mechanism.
	s.Register("/vm/passthrough-egress", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req VMEgressPassthroughRequest
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

		_, wasOpen := egressPassthroughState.get(req.Name)
		policyAuthority.SetMode(req.Name, ModePassthrough)

		dur := time.Duration(req.DurationSeconds) * time.Second
		if dur <= 0 {
			dur = defaultPassthroughSeconds * time.Second
		}
		egressPassthroughState.put(req.Name, time.Now().Add(dur))
		armPassthroughRestoreTimer(locks, req.Name, dur)

		writeJSON(w, VMEgressPassthroughResponse{
			WasOpen:        wasOpen,
			ExpiresSeconds: int(dur / time.Second),
		})
	})

	// /vm/restrict-egress closes an active passthrough window: flips
	// the policy authority back to ModeRestricted, cancels the
	// restore timer, deletes state. No-op (was_open=false) if no
	// window is active — matches `devm restrict` idempotency
	// contract from the spec. Softnet and iron-proxy are untouched.
	s.Register("/vm/restrict-egress", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req VMApplyEgressEnforcementRequest // reuses {Name} shape
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

		_, wasOpen := egressPassthroughState.get(req.Name)
		if !wasOpen {
			writeJSON(w, VMEgressRestrictResponse{WasOpen: false})
			return
		}

		policyAuthority.SetMode(req.Name, ModeRestricted)
		egressPassthroughState.del(req.Name)
		writeJSON(w, VMEgressRestrictResponse{WasOpen: true})
	})

	// /vm/egress-status returns whether a passthrough window is
	// currently active for the project. Read-only: never mutates
	// state. Suitable for `devm status` polling.
	s.Register("/vm/egress-status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, "name query param required", http.StatusBadRequest)
			return
		}
		entry, ok := egressPassthroughState.get(name)
		resp := EgressStatus{Policy: "restricted"}
		if ok {
			resp.Policy = "passthrough"
			expiresAt := entry.expiresAt
			resp.PassthroughExpiresAt = &expiresAt
		}
		writeJSON(w, resp)
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

// pickPort returns a free ephemeral TCP port: bind to :0 on 0.0.0.0
// (all interfaces), read back the assigned port, and close. There is a
// small TOCTOU window between the close and the subsequent bind
// (iron-proxy's, or another caller's) — standard on darwin where
// SO_REUSEPORT can't be shared across processes.
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

// tartGuestExec returns a GuestExec that runs script inside name via
// `tart exec -i <name> sudo bash -s`, script on stdin — the transport
// SetupVolumesPhase/SetupReposPhase need for their guest-side scans,
// clones, and mirror-dir checks.
func tartGuestExec(ctx context.Context, tr *tart.Tart, name string) GuestExec {
	return func(script string) (stdout, stderr string, exitCode int, err error) {
		res := tr.ExecStdin(ctx, name, strings.NewReader(script), []string{"sudo", "bash", "-s"})
		return res.Stdout, res.Stderr, res.ExitCode, nil
	}
}

// guestGitCACertPath returns the guest-side CA bundle path git trusts
// for a proxied clone — the merged system trust store devm exports as
// GIT_SSL_CAINFO (see internal/caenv's "SSL_CERT_FILE trap" warning
// against pointing at the raw devm.crt instead of the merged bundle).
func guestGitCACertPath() string {
	for _, v := range caenv.Vars {
		if v.Key == "GIT_SSL_CAINFO" {
			return v.Value
		}
	}
	return ""
}

// sendSoftnetEnforced flips a project's softnet control socket to
// ENFORCED, forwarding egress to iron-proxy's HTTP/HTTPS/DNS listeners and
// the daemon's SNTP responder. All four addresses are loopback: softnet
// dials iron-proxy and the NTP responder host-side, so the endpoint it
// sends is always loopback.
func sendSoftnetEnforced(sock string, info projectInfo, ntpPort int) error {
	return newSoftnetClient(sock).setPolicy("ENFORCED", endpointFrom(info, ntpPort))
}

// endpointFrom builds the loopback softnet Endpoint for a project's
// stashed projectInfo and the daemon's SNTP responder port. Every
// setPolicy push — OPEN or ENFORCED, CLI-driven or daemon-restart
// reconcile — goes through this builder so the wire shape is always
// complete: setPolicy keeps the previous endpoint on a nil push, so a
// caller building a partial Endpoint by hand would silently clobber
// whichever fields it left zero.
//
// GuestHTTP/GuestHTTPS are left "" when the corresponding port is 0
// (the guest-origin listener pair hasn't bound yet) rather than
// resolved through ironProxyListenAddr, which would otherwise produce
// "127.0.0.1:0" — a value that dials nothing but isn't empty either, so
// softnet's ft.GuestHTTP != "" deny guard can never fire on it. An
// empty string is unambiguous: softnet denies guest-originated `.test`
// traffic cleanly instead of the guest hanging on a dial to :0.
func endpointFrom(info projectInfo, ntpPort int) *Endpoint {
	e := &Endpoint{
		HTTP:  ironProxyListenAddr(info.HTTPPort),
		HTTPS: ironProxyListenAddr(info.HTTPSPort),
		DNS:   ironProxyListenAddr(info.DNSPort),
		NTP:   ironProxyListenAddr(ntpPort),
	}
	if info.GuestHTTPPort != 0 {
		e.GuestHTTP = ironProxyListenAddr(info.GuestHTTPPort)
	}
	if info.GuestHTTPSPort != 0 {
		e.GuestHTTPS = ironProxyListenAddr(info.GuestHTTPSPort)
	}
	if info.PopPort != 0 {
		e.Pop = ironProxyListenAddr(info.PopPort)
	}
	return e
}

// projectInfo is the daemon's per-project state registry, keyed by
// projectID in ironProxyState (kept its historical variable name for
// diff hygiene). Fields survive daemon restart via StateSnapshot mirror
// so AdoptIronProxies can rebuild after a crash.
type projectInfo struct {
	HTTPPort   int
	HTTPSPort  int
	TunnelPort int
	DNSPort    int

	// GuestHTTPPort / GuestHTTPSPort are the daemon's guest-origin listener
	// pair for this project — where softnet forwards `.test` traffic.
	// In-memory only: the listeners die with the daemon, so a restart
	// rebinds a fresh pair and re-pushes it (see rebindProjectListeners).
	GuestHTTPPort  int
	GuestHTTPSPort int

	// PopPort is the daemon's per-project pop HTTP listener — where
	// softnet forwards guest TCP 192.168.127.1:81. In-memory only, set
	// at /vm/start and cleared at /vm/stop via closePopListener.
	PopPort int

	// ProjectIP is the project's allocated 127.42/16 loopback IP. All
	// ingress listeners (softnet direct ports, softnet SSH, daemon HTTP
	// proxy) bind on this IP. Allocated at /vm/start via
	// AllocateProjectIP; released at /vm/stop via ReleaseProjectIP.
	// Empty when the project is stopped.
	ProjectIP string
}

type projectInfoStore struct {
	mu sync.Mutex
	m  map[string]projectInfo
}

func newIronProxyStore() *projectInfoStore {
	return &projectInfoStore{m: make(map[string]projectInfo)}
}

func (s *projectInfoStore) put(projectID string, info projectInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[projectID] = info
}

func (s *projectInfoStore) get(projectID string) (projectInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[projectID]
	return v, ok
}

func (s *projectInfoStore) del(projectID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, projectID)
}

// keys returns every project id currently tracked. Used by
// discoverSoftnet to walk the projects AdoptIronProxies has just
// rehydrated on daemon restart and rebuild softnetState for each.
func (s *projectInfoStore) keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.m))
	for k := range s.m {
		out = append(out, k)
	}
	return out
}

var ironProxyState = newIronProxyStore()
