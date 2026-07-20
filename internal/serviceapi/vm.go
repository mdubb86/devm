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

	"github.com/mdubb86/devm/internal/debuglog"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
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
// collide with a real destination. Under ENFORCED policy, softnet forwards
// outbound TCP:80/443 to iron-proxy's listeners purely by destination
// port, regardless of destination IP, so traffic addressed to the
// sentinel reaches iron-proxy the same way traffic to a real address
// would.
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
}

// VMStopRequest is the body shape for POST /vm/stop. The daemon calls
// `tart stop <Name>` for a graceful guest shutdown before SIGTERM'ing the
// supervised tart process.
type VMStopRequest struct {
	Name string `json:"name"`
}

// VMConfigLockRequest is the body shape for POST /vm/unlock-config and
// POST /vm/lock-config. RelockSeconds is only meaningful to
// /vm/unlock-config: how long to leave devm.yaml editable before the
// daemon re-locks it automatically. Zero means "use
// defaultRelockSeconds".
type VMConfigLockRequest struct {
	Name          string `json:"name"`
	RelockSeconds int    `json:"relock_seconds,omitempty"`
}

// VMConfigLockResponse is the response for POST /vm/unlock-config and
// POST /vm/lock-config. WasLocked reports whether the project had a
// configLockState entry — i.e. whether there was anything to
// unlock/lock. false (with no error) means the VM isn't running or
// config_lock is disabled for the project. RelockSeconds is only set
// by /vm/unlock-config when WasLocked: the duration the just-armed
// auto-relock timer will wait before re-locking devm.yaml.
type VMConfigLockResponse struct {
	WasLocked     bool `json:"was_locked"`
	RelockSeconds int  `json:"relock_seconds,omitempty"`
}

// VMEnforcementConfigResponse is the body shape for GET
// /vm/enforcement-config: everything the boot-integrity-gate composed
// provisioning script still bakes into its enforce phase. Egress
// allow-listing and DNS resolution are enforced by softnet over the
// control socket (POST /vm/apply-egress-enforcement), not by guest-side
// nftables/dnsmasq. TimesyncdScript still points the guest's
// systemd-timesyncd at the proxy sentinel — softnet's UDP forwarder
// catches outbound udp:123 regardless of destination, but the guest must
// still be configured to send NTP somewhere for that interception to
// matter.
type VMEnforcementConfigResponse struct {
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

// armRelockTimer schedules devm.yaml to be re-locked after d, the
// bound `devm unlock --for <dur>` (or the default) puts on an
// unattended unlock window. Installing it via configLockState.setTimer
// stops+replaces whatever relock timer was already pending for name,
// so repeated unlocks (or a lock/reconcile in between) never leave two
// timers racing.
//
// The callback runs on its own goroutine (time.AfterFunc, not inline),
// so taking locks.Lock(name) here is not nested under any handler's
// lock. It re-checks configLockState and the VM's running state right
// before locking — by the time it fires, the project may have been
// stopped, torn down, or already re-locked by a `devm lock` or
// `devm reconcile` in the interim, both of which call stopTimer and so
// would have already cancelled this timer; the re-check is therefore
// belt-and-suspenders against the timer having fired the instant
// before a racing cancellation.
func armRelockTimer(locks *ProjectLocks, tr TartLister, name string, d time.Duration) {
	t := time.AfterFunc(d, func() {
		unlock := locks.Lock(name)
		defer unlock()

		e, ok := configLockState.get(name)
		if !ok {
			return // stopped/torn down since unlock — nothing to relock
		}
		vms, err := tr.List(context.Background())
		if err != nil {
			// Fail closed: a transient `tart list` error means we can't
			// confirm the VM is stopped, so re-lock rather than leave a
			// running VM's config writable (the invariant this lock exists
			// for). Worst case is a stale lock on an already-stopped VM,
			// which the next `devm stop`/`devm unlock` clears — recoverable,
			// unlike a silently-unlocked running VM.
			debuglog.Logf("configlock", "auto-relock %s: tart list failed, re-locking fail-closed: %v", name, err)
		} else if !vmRunning(vms, name) {
			return // VM confirmed stopped — don't strand a lock on it
		}
		if err := lockConfigFiles(e.repoRoot); err != nil {
			debuglog.Logf("configlock", "auto-relock %s: %v", name, err)
		}
	})
	configLockState.setTimer(name, t)
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
func RegisterVMHandlers(s *Server, sup *supervisor.Supervisor, tr *tart.Tart, denials *Denials, ntpPort int, locks *ProjectLocks, proxy *ProxyServer) {
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
		// Make devm.yaml (+ devm.me.yaml) host-immutable before the guest
		// ever boots, so a root guest never sees a writable window onto its
		// own trust boundary. Best-effort: a chflags failure must not block
		// the VM from starting; config_lock:false opts a project out
		// entirely.
		if req.Cfg.ConfigLockEnabled() {
			if err := lockConfigFiles(req.WorkspaceHostPath); err != nil {
				debuglog.Logf("configlock", "lock config for %s: %v (continuing)", req.Name, err)
			} else {
				configLockState.put(req.Name, req.WorkspaceHostPath)
			}
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
		if err := ensureSoftnetSockDir(softnetSockDir()); err != nil {
			http.Error(w, fmt.Sprintf("softnet sock dir: %v", err), http.StatusInternalServerError)
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

		// Allocate the per-project loopback IP (127.42.0.N). All ingress
		// listeners for this project — softnet's direct-service ports,
		// softnet SSH on :22, and this daemon's own per-project HTTP/HTTPS
		// proxy listeners — bind on this address. Idempotent: a project
		// that already has one (e.g. re-`devm shell` on an already-running
		// VM) keeps it.
		projectIP, err := AllocateProjectIP(req.Name)
		if err != nil {
			http.Error(w, fmt.Sprintf("allocate project ip: %v", err), http.StatusInternalServerError)
			return
		}

		// Push the initial ingress expose map. Independent of egress
		// state; listeners bind on the host and forward lazily once
		// guest services come up. Non-fatal: egress is the security
		// boundary, ingress is convenience, and a failed push is
		// re-attempted at the next reconcile.
		if err := pushExposeMap(req.Name, computeExposeMap(req.Cfg, projectIP)); err != nil {
			debuglog.Logf("serviceapi", "vm/start: push expose map for %s: %v", req.Name, err)
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
			// never a real destination); softnet forwards outbound
			// TCP:80/443 to iron-proxy purely by destination port under
			// ENFORCED policy, so traffic to the sentinel reaches
			// iron-proxy the same as any other allow-listed destination.
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

		// Stash port info for VM env injection and the deferred
		// egress-enforcement inject to read. Merge onto the existing
		// entry rather than overwrite — AllocateProjectIP above already
		// stashed ProjectIP, and a raw put here would silently clobber it
		// back to empty.
		info, _ := ironProxyState.get(req.Name)
		info.HTTPPort = httpPort
		info.HTTPSPort = httpsPort
		info.DNSPort = dnsPort
		ironProxyState.put(req.Name, info)

		// Apply VM-side config via tart exec — workspace mount, extra
		// mounts, env only. The iron-proxy egress-enforcement config
		// (timesyncd) is fetched by the CLI orchestrator via GET
		// /vm/enforcement-config and baked into the composed provisioning
		// script's enforce phase, so the user's install:, apt-get, and
		// template-install steps still run with open egress — iron-proxy
		// is meant to gate the workload/services, not the developer's
		// provisioning phase.
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

		// Bind this project's per-project HTTP/HTTPS proxy listeners on
		// its ProjectIP via the helper. Non-fatal like the
		// expose-map push above: ingress is convenience, not the security
		// boundary, and a failed bind (e.g. the helper isn't
		// installed) is re-attempted on the next /vm/start.
		if proxy != nil {
			if err := proxy.StartProjectListeners(ctx, req.Name, projectIP); err != nil {
				debuglog.Logf("serviceapi", "vm/start: start project listeners for %s: %v", req.Name, err)
			}
		}

		writeJSON(w, VMStartResponse{ProjectIP: projectIP})
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

	// /vm/open-egress flips a project's softnet control socket to OPEN —
	// unrestricted egress for the provisioning window (apt, install:,
	// templates, startup:) before the enforced allowlist is in place.
	// softnet boots LOCKED, so cold-start provisioning would otherwise run
	// with no egress at all.
	s.Register("/vm/open-egress", func(w http.ResponseWriter, r *http.Request) {
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

		if err := newSoftnetClient(sock).setPolicy("OPEN", nil); err != nil {
			http.Error(w, fmt.Sprintf("flip softnet open: %v", err), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
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

		sock := softnetState.get(req.Name)
		if sock == "" {
			http.Error(w, "softnet control socket missing — was /vm/start called for this project?",
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

		if err := sendSoftnetEnforced(sock, info, ntpPort); err != nil {
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
		// Close this project's per-project HTTP/HTTPS proxy listeners
		// before releasing its IP — the IP must not be handed to another
		// project's /vm/start while this project might still be
		// listening on it.
		if proxy != nil {
			proxy.StopProjectListeners(req.Name)
		}
		ReleaseProjectIP(req.Name)
		ironProxyState.del(req.Name)
		// The softnet client is stateless — it dials fresh per call rather
		// than holding a persistent connection — so there's nothing to
		// close here, only the daemon's record of the socket path to drop.
		softnetState.del(req.Name)
		// A stopped project frees its claimed host ports for other
		// projects to take.
		exposeClaims.release(req.Name)
		if denials != nil {
			denials.Reset(req.Name)
		}

		// Unlock devm.yaml (+ devm.me.yaml) unconditionally-if-known: a
		// no-op when locking was disabled (config_lock:false) or never
		// happened, since no repoRoot resolves in that case. The registry
		// is the normal source; the state snapshot's WorkspaceHostPath is
		// the fallback for a project adopted across a daemon restart that
		// hasn't gone through /vm/start (and thus recoverProjectState)
		// again yet.
		repoRoot := ""
		if e, ok := configLockState.get(req.Name); ok {
			repoRoot = e.repoRoot
		} else if snap, _ := ReadStateSnapshot(req.Name); snap != nil {
			repoRoot = snap.WorkspaceHostPath
		}
		if repoRoot != "" {
			if err := unlockConfigFiles(repoRoot); err != nil {
				debuglog.Logf("configlock", "unlock config for %s: %v", req.Name, err)
			}
		}
		configLockState.del(req.Name)

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

	// /vm/unlock-config is the `devm unlock` escape hatch: it lifts the
	// host-immutable flag on devm.yaml (+ devm.me.yaml) for a running
	// project so the user can edit config without the daemon fighting
	// them, and arms a relock timer bounding how long it stays editable
	// unattended (`--for <dur>`, default defaultRelockSeconds). `devm
	// lock` or `devm reconcile` ends the window early and cancels this
	// timer (stopTimer). A project with no configLockState entry (VM
	// not running, or config_lock:false) is a no-op, not an error —
	// WasLocked reports which case this was.
	s.Register("/vm/unlock-config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req VMConfigLockRequest
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

		entry, ok := configLockState.get(req.Name)
		relockSeconds := 0
		if ok {
			if err := unlockConfigFiles(entry.repoRoot); err != nil {
				debuglog.Logf("configlock", "unlock config for %s: %v (continuing)", req.Name, err)
			}
			d := time.Duration(req.RelockSeconds) * time.Second
			if d <= 0 {
				d = defaultRelockSeconds * time.Second
			}
			armRelockTimer(locks, tr, req.Name, d)
			relockSeconds = int(d / time.Second)
		}

		writeJSON(w, VMConfigLockResponse{WasLocked: ok, RelockSeconds: relockSeconds})
	})

	// /vm/lock-config is the `devm lock` command: re-locks devm.yaml on
	// demand, ending a temporary unlock early instead of waiting for the
	// relock timer.
	s.Register("/vm/lock-config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req VMConfigLockRequest
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

		entry, ok := configLockState.get(req.Name)
		if ok {
			if err := lockConfigFiles(entry.repoRoot); err != nil {
				debuglog.Logf("configlock", "lock config for %s: %v (continuing)", req.Name, err)
			}
			configLockState.stopTimer(req.Name)
		}

		writeJSON(w, VMConfigLockResponse{WasLocked: ok})
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

// sendSoftnetEnforced flips a project's softnet control socket to
// ENFORCED, forwarding egress to iron-proxy's HTTP/HTTPS/DNS listeners and
// the daemon's SNTP responder. All four addresses are loopback: softnet
// dials iron-proxy and the NTP responder host-side, so the endpoint it
// sends is always loopback.
func sendSoftnetEnforced(sock string, info projectInfo, ntpPort int) error {
	return newSoftnetClient(sock).setPolicy("ENFORCED", endpointFrom(info, ntpPort))
}

// endpointFrom builds the loopback softnet Endpoint for a project's
// stashed projectInfo and the daemon's SNTP responder port. Shared by
// sendSoftnetEnforced (the CLI-driven /vm/apply-egress-enforcement step)
// and discoverSoftnet (the daemon-restart reconcile pass) so both push
// the same wire shape.
func endpointFrom(info projectInfo, ntpPort int) *Endpoint {
	return &Endpoint{
		HTTP:  ironProxyListenAddr(info.HTTPPort),
		HTTPS: ironProxyListenAddr(info.HTTPSPort),
		DNS:   ironProxyListenAddr(info.DNSPort),
		NTP:   ironProxyListenAddr(ntpPort),
	}
}

// projectInfo is the daemon's per-project state registry, keyed by
// projectID in ironProxyState (kept its historical variable name for
// diff hygiene). Fields survive daemon restart via StateSnapshot mirror
// so AdoptIronProxies can rebuild after a crash.
type projectInfo struct {
	HTTPPort  int
	HTTPSPort int
	DNSPort   int

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
