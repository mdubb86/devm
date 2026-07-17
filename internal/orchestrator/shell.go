package orchestrator

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mdubb86/devm/internal/debuglog"
	"github.com/mdubb86/devm/internal/devmbundle"
	"github.com/mdubb86/devm/internal/docker"
	"github.com/mdubb86/devm/internal/ironproxy"
	"github.com/mdubb86/devm/internal/provision"
	"github.com/mdubb86/devm/internal/render"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/secret"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/mdubb86/devm/internal/serviceapi/sshkeys"
	"github.com/mdubb86/devm/internal/status"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// resolveSecretBindings gathers every `!secret <name>` ref from cfg
// (top-level env + per-service env, deduped), looks each up in the macOS
// login keychain under "<project>/<name>", and attaches the injection-host
// scope declared in network.allow. Returns the bindings the daemon hands
// to iron-proxy. A secret with no network.allow host scope is still
// resolved and sent with empty Hosts (iron-proxy omits it — never injects).
//
// Runs CLI-side because the daemon (a LaunchDaemon) can't access the
// user's login keychain.
func resolveSecretBindings(cfg schema.Config, backend secret.Backend) ([]serviceapi.SecretBinding, error) {
	seen := map[string]bool{}
	var names []string
	collect := func(env map[string]schema.EnvValue) {
		for _, v := range env {
			if v.Secret != nil && !seen[v.Secret.Name] {
				seen[v.Secret.Name] = true
				names = append(names, v.Secret.Name)
			}
		}
	}
	collect(cfg.Env)
	for _, svc := range cfg.Services {
		collect(svc.Env)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, nil
	}

	hosts := cfg.Network.SecretHosts()
	var bindings []serviceapi.SecretBinding
	var missing []string
	for _, n := range names {
		v, err := backend.Get(cfg.Project.Name + "/" + n)
		if err != nil {
			missing = append(missing, n)
			continue
		}
		bindings = append(bindings, serviceapi.SecretBinding{Name: n, Value: v, Hosts: hosts[n]})
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing secrets in keychain: %v (set with `devm secret set <name>`)", missing)
	}
	return bindings, nil
}

// ShellDeps wires the orchestrator's collaborators. Production callers
// build one via DefaultShellDeps; tests substitute fakes.
type ShellDeps struct {
	Tart             *tart.Tart
	ServiceAPIClient VMAdminClient
	// UserSpawner runs the interactive shell command. Production code
	// uses ExecSpawner; tests use a stub.
	UserSpawner Spawner
}

// VMAdminClient is the subset of serviceapi.Client used by RunShell.
// Extracted as an interface so tests can inject a fake.
type VMAdminClient interface {
	VMStatus(ctx context.Context, name string) (serviceapi.VMStatusResponse, error)
	StartVM(ctx context.Context, req serviceapi.VMStartRequest) error
	EnforcementConfig(ctx context.Context, name string) (serviceapi.VMEnforcementConfigResponse, error)
	StopVM(ctx context.Context, name string) error
	// ApplyIronProxy (re)spawns this project's iron-proxy on its
	// existing MAC_HOST/ports without touching the VM — the same
	// no-VM-cycle primitive `devm reconcile`'s self-heal
	// (BucketEgressRestart) uses. Adopt-in-place needs it: a prior
	// `devm stop` tears iron-proxy down along with the VM, so a VM
	// adopted after a raw `tart run` may have no live iron-proxy even
	// though the VM process itself is up.
	ApplyIronProxy(ctx context.Context, req serviceapi.VMApplyIronProxyRequest) (serviceapi.VMApplyIronProxyResponse, error)
}

// DefaultShellDeps returns deps wired for production.
func DefaultShellDeps(repoRoot string) ShellDeps {
	return ShellDeps{
		Tart:             tart.New(),
		ServiceAPIClient: serviceapi.NewClient(),
		UserSpawner:      &ExecSpawner{Interactive: true},
	}
}

// RunShell implements `devm shell`. Returns the user shell's exit code
// and a non-nil error only when an orchestration step itself failed.
func RunShell(ctx context.Context, d ShellDeps, cfg schema.Config, repoRoot, vmName, cmdName string, cmdArgs []string) (int, error) {
	reporter := status.New(os.Stderr)
	defer reporter.Stop()
	reporter.Start("starting up")

	// Check VM state via daemon admin.
	vmStatus, err := d.ServiceAPIClient.VMStatus(ctx, cfg.Project.Name)
	if err != nil {
		return -1, fmt.Errorf("query vm status: %w", err)
	}
	debuglog.Logf("shell", "vm status: present=%v running=%v", vmStatus.Present, vmStatus.Running)

	if vmStatus.Running {
		// The VM process is up, but that alone doesn't tell us whether it's
		// provisioned. Probe devm.target (gates access until provisioning's
		// service-start phase succeeds) to find out which of three states
		// we're in.
		//
		// ExecWithRetry, not Exec: this probe drives the security-sensitive
		// warm/adopt/dirty branch below. A transient guest-agent transport
		// flake here (ExitCode -1) would misread a warm, provisioned VM as
		// "not provisioned", falling into the dirty/adopt checks and risking
		// a needless re-provision. A genuine "not active" is a clean
		// non-zero exit (not a transport flake), so it is not retried.
		provisioned := d.Tart.ExecWithRetry(ctx, vmName,
			[]string{"systemctl", "is-active", "devm.target"}).ExitCode == 0
		if provisioned {
			return d.warmAttach(ctx, vmName, repoRoot, cmdName, cmdArgs, reporter)
		}

		// Not provisioned. /run/devm/provisioning is written before the
		// composed script starts and removed when it finishes (render's
		// inProgressMarker) — its presence means a previous provisioning
		// run was interrupted (daemon crash, host sleep, killed exec) and
		// left the guest in an unknown intermediate state.
		//
		// ExecWithRetry, not Exec: a transport flake here (ExitCode -1)
		// would misread a dirty VM as clean, adopting-in-place onto an
		// unknown intermediate state instead of tearing it down.
		dirty := d.Tart.ExecWithRetry(ctx, vmName,
			[]string{"test", "-f", "/run/devm/provisioning"}).ExitCode == 0
		if dirty {
			// Never provision onto a dirty slate: tear down and fall
			// through to a fresh cold start below.
			reporter.Step("recovering (teardown + fresh start)", false)
			if err := d.teardownVM(ctx, cfg, vmName); err != nil {
				return -1, fmt.Errorf("teardown dirty vm: %w", err)
			}
		} else {
			// Pristine: running but never provisioned (direct `tart run`,
			// or a clean daemon crash-restart before provisioning began).
			// Adopt in place — provision it without StartVM/waitVMReady,
			// since it's already up and exec-ready.
			reporter.Step("adopting running vm", false)
			bindings, err := resolveSecretBindings(cfg, secret.NewMacKeychain())
			if err != nil {
				return -1, fmt.Errorf("resolve secrets: %w", err)
			}
			// Adopt-in-place deliberately skips StartVM below (the VM
			// process is already up), but StartVM is also the only
			// thing that normally (re)spawns this project's iron-proxy.
			// Revive it explicitly on its last-known MAC_HOST/ports so
			// the provisioning tail's EnforcementConfig fetch (next,
			// inside provisionAndAttach) has a live iron-proxy to read.
			applyResp, err := d.ServiceAPIClient.ApplyIronProxy(ctx, serviceapi.VMApplyIronProxyRequest{
				Name:      cfg.Project.Name,
				Allowlist: docker.EffectiveAllowlist(cfg),
				Secrets:   bindings,
			})
			if err != nil {
				return -1, fmt.Errorf("ensure iron-proxy for adopt-in-place: %w", err)
			}
			if !applyResp.Applied && !applyResp.VMRunning {
				return -1, fmt.Errorf(
					"adopt-in-place: no iron-proxy record found for %q — this vm was never started by devm",
					cfg.Project.Name)
			}
			return d.provisionAndAttach(ctx, cfg, vmName, repoRoot, cmdName, cmdArgs, bindings, reporter)
		}
	}

	// Cold start: the VM was stopped, or we just tore down a dirty one above.
	reporter.Step("starting vm", false)
	debuglog.Logf("shell", "cold-start: sending StartVM to daemon")

	// Collect allow-list from network config, expanded with Docker Hub
	// hosts when docker: true.
	allowList := docker.EffectiveAllowlist(cfg)

	bindings, err := resolveSecretBindings(cfg, secret.NewMacKeychain())
	if err != nil {
		return -1, fmt.Errorf("resolve secrets: %w", err)
	}

	// Resolve each mounts[] entry against repoRoot (~ expansion, relative→
	// absolute, :ro suffix passthrough). schema.ValidateWithRoot already
	// rejected malformed entries at config-load time; ResolveMount here
	// just canonicalises for the daemon.
	extraMounts := make([]string, 0, len(cfg.Mounts))
	for i, entry := range cfg.Mounts {
		resolved, err := schema.ResolveMount(entry, repoRoot)
		if err != nil {
			return -1, fmt.Errorf("mounts[%d]: %w", i, err)
		}
		extraMounts = append(extraMounts, resolved)
	}

	diskGB, _ := cfg.DiskSizeGB()
	if err := d.ServiceAPIClient.StartVM(ctx, serviceapi.VMStartRequest{
		Name:              cfg.Project.Name,
		WorkspaceHostPath: repoRoot,
		AllowList:         allowList,
		Secrets:           bindings,
		ExtraMounts:       extraMounts,
		DiskSizeGB:        diskGB,
		Docker:            cfg.Docker,
	}); err != nil {
		return -1, fmt.Errorf("start vm: %w", err)
	}

	// Wait for VM to accept exec connections.
	reporter.Step("waiting for vm ready", false)
	if err := waitVMReady(ctx, d.Tart, vmName, 60*time.Second); err != nil {
		return d.teardownOnFail(ctx, cfg, vmName, err, "vm did not become ready")
	}
	debuglog.Logf("shell", "cold-start: vm exec-ready")

	return d.provisionAndAttach(ctx, cfg, vmName, repoRoot, cmdName, cmdArgs, bindings, reporter)
}

// warmAttach attaches to a VM that's already provisioned (devm.target
// active) — no reconciliation, no provisioning, just attach.
func (d ShellDeps) warmAttach(ctx context.Context, vmName, repoRoot, cmdName string, cmdArgs []string, reporter status.Reporter) (int, error) {
	// Warm attach: reconcile is handled by the provisioner on cold start.
	// For now the warm path just attaches directly.
	reporter.Step("attaching to running vm", false)
	reporter.Step("ready", false)
	reporter.Stop()
	reporter.Clear()
	if err := EmitSSHConfig(ctx, d.Tart); err != nil {
		log.Printf("ssh_config emit failed on warm attach: %v", err)
	}
	return d.attachShell(ctx, vmName, repoRoot, cmdName, cmdArgs)
}

// provisionAndAttach runs the provisioning + attach tail shared by
// cold-start (called after StartVM/waitVMReady) and adopt-in-place (called
// directly — the VM is already running and exec-ready). Any failure here
// tears the VM down unless it's a post-install failure, in which case the
// VM is kept running for in-place debugging (test_51's contract).
func (d ShellDeps) provisionAndAttach(ctx context.Context, cfg schema.Config, vmName, repoRoot, cmdName string, cmdArgs []string, bindings []serviceapi.SecretBinding, reporter status.Reporter) (int, error) {
	caPEM, err := os.ReadFile(filepath.Join(caStorageDir(), "root.crt"))
	if err != nil {
		return d.teardownOnFail(ctx, cfg, vmName, err, "read CA root")
	}
	authPub, err := sshkeys.EnsureProjectKeypair(cfg.Project.Name)
	if err != nil {
		return d.teardownOnFail(ctx, cfg, vmName, err, "ensure project ssh keypair")
	}
	hostPriv, hostPub, err := sshkeys.EnsureProjectHostKey(cfg.Project.Name)
	if err != nil {
		return d.teardownOnFail(ctx, cfg, vmName, err, "ensure project ssh host key")
	}

	// The enforcement config (nft allowlist + dnsmasq + timesyncd) is baked
	// into the composed provisioning script's enforce phase (so DNS/NTP/
	// egress all come up under enforcement in one exec). The daemon
	// computes it per project from the iron-proxy MAC_HOST/ports stashed
	// at StartVM.
	enforcement, err := d.ServiceAPIClient.EnforcementConfig(ctx, cfg.Project.Name)
	if err != nil {
		return d.teardownOnFail(ctx, cfg, vmName, err, "fetch enforcement config")
	}

	prov := &provision.Provisioner{
		Tart:                d.Tart,
		VMName:              vmName,
		Cfg:                 cfg,
		CARootPEM:           caPEM,
		SSHAuthorizedPubkey: authPub,
		SSHHostPriv:         hostPriv,
		SSHHostPub:          hostPub,
		WorkspaceVMPath:     repoRoot,
		EnforcedNft:         enforcement.NftRuleset,
		DnsmasqScript:       enforcement.DnsmasqScript,
		TimesyncdScript:     enforcement.TimesyncdScript,
		StepTimeoutSeconds:  installStepTimeoutSeconds(),
	}
	debuglog.Logf("shell", "provisioning %s", vmName)
	reporter.Step("provisioning", false)
	// Provisioning output is DIAGNOSTIC — stage names, package install
	// noise, etc. It belongs on stderr so `devm exec pwd` / `devm shell
	// -- <cmd>` produce clean stdout that scripts can pipe. Failure details
	// flow via the returned error plus pp.FailureOutput() below, not via
	// this writer. pp drives the stage-marker spinner from ExecStream's
	// line-by-line output.
	pp := newProvisionProgress(reporter)
	if err := prov.Run(ctx, os.Stderr, pp.Line); err != nil {
		fmt.Fprint(os.Stderr, pp.FailureOutput())
		// Service-phase failures (unit install, daemon-reload, enable+start,
		// apply masks) leave the VM in a debuggable state — user's fix is
		// in devm.yaml, not in the VM. Surface the error but keep the VM
		// alive so `tart exec <vm> systemctl status` etc. works. Pre-service
		// failures tear down (test_51: install failure = state=absent).
		if provision.IsPostInstallFailure(err) {
			debuglog.Logf("shell", "post-install failure — keeping VM: %v", err)
			return -1, fmt.Errorf("provision: %w", err)
		}
		return d.teardownOnFail(ctx, cfg, vmName, err, "provision")
	}
	debuglog.Logf("shell", "provisioning done: %s", vmName)

	// Write initial snapshot so that subsequent `devm reconcile` calls have
	// a baseline to diff against. Without this, ReadSnapshot returns "" which
	// reconcile treats as zero-diff (identity with the new config), masking
	// any changes made between cold-start and the first reconcile.
	provSnap, err := yaml.Marshal(cfg)
	if err != nil {
		return d.teardownOnFail(ctx, cfg, vmName, err, "marshal provision snapshot")
	}
	if err := WriteSnapshot(d.Tart, vmName, snapshotHeader+string(provSnap)); err != nil {
		return d.teardownOnFail(ctx, cfg, vmName, err, "write provision snapshot")
	}
	debuglog.Logf("shell", "snapshot written: %s", vmName)

	// Seed the daemon-side state snapshot too, now that provisioning is
	// fully green (provisioning AND egress enforcement, which runs as
	// a step inside prov.Run, both succeeded). Without this, the first
	// `devm reconcile` after `devm start` finds no baseline, diffs
	// against schema.Config{}, and every teardown-bucket kind
	// spuriously surfaces as pending — prompting the user to tear down
	// the VM they just started. Best-effort: log but don't fail here —
	// a missing snapshot only degrades to "full diff on next
	// reconcile" (safe), and failing here would kill a start that
	// otherwise succeeded.
	templateContents, err := render.RenderTemplatesByBasename(cfg, repoRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "state: render templates for seed snapshot %s failed: %v\n", cfg.Project.Name, err)
	}
	snap := serviceapi.StateSnapshot{
		Cfg:              cfg,
		TemplateContents: templateContents,
		SecretHashes:     SecretHashesFromBindings(bindings),
		ProxyVersion:     ironproxy.EmbeddedSha256(), // stamp the version that just provisioned
	}
	if err := serviceapi.WriteStateSnapshot(cfg.Project.Name, snap); err != nil {
		fmt.Fprintf(os.Stderr, "state: seed snapshot for %s failed: %v\n", cfg.Project.Name, err)
	}

	if err := EmitSSHConfig(ctx, d.Tart); err != nil {
		log.Printf("ssh_config emit failed after provisioning: %v", err)
	}

	reporter.Step("ready", false)
	reporter.Stop()
	reporter.Clear()

	return d.attachShell(ctx, vmName, repoRoot, cmdName, cmdArgs)
}

// defaultInstallStepTimeoutSeconds is installStepTimeoutSeconds' fallback
// when DEVM_INSTALL_STEP_TIMEOUT_S is unset or invalid. Matches
// render.defaultStepTimeoutSeconds.
const defaultInstallStepTimeoutSeconds = 600

// installStepTimeoutSeconds reads DEVM_INSTALL_STEP_TIMEOUT_S — the e2e
// suite's override for the composed script's install:/startup: `timeout`
// budget — falling back to defaultInstallStepTimeoutSeconds when the var is
// unset or not a positive integer. Mirrors the old per-step provisioner's
// os.Getenv("DEVM_INSTALL_STEP_TIMEOUT_S") handling.
func installStepTimeoutSeconds() int {
	v := os.Getenv("DEVM_INSTALL_STEP_TIMEOUT_S")
	if v == "" {
		return defaultInstallStepTimeoutSeconds
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return defaultInstallStepTimeoutSeconds
	}
	return n
}

// teardownVM stops and deletes vmName via the daemon + tart. Used both by
// teardownOnFail (a provisioning-time failure) and directly by RunShell
// when it finds the VM in a dirty (interrupted-provisioning) state and
// must destroy it before a fresh cold start — never provision onto a
// dirty slate.
func (d ShellDeps) teardownVM(ctx context.Context, cfg schema.Config, vmName string) error {
	teardownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if stopErr := d.ServiceAPIClient.StopVM(teardownCtx, cfg.Project.Name); stopErr != nil {
		// StopVM is best-effort here (VM may be already stopped or gone),
		// but if it errored for a reason worth diagnosing, we want the
		// caller to see it — otherwise this class of failure (VM stopped
		// but not deleted) is invisible from the outside.
		fmt.Fprintf(os.Stderr, "teardown: StopVM: %v\n", stopErr)
		debuglog.Logf("shell", "teardown: StopVM: %v", stopErr)
	}
	if derr := d.Tart.Delete(teardownCtx, vmName); derr != nil &&
		!strings.Contains(derr.Error(), "does not exist") {
		fmt.Fprintf(os.Stderr, "teardown: tart delete %s failed: %v\n", vmName, derr)
		debuglog.Logf("shell", "teardown: delete %s failed: %v", vmName, derr)
		return fmt.Errorf("tart delete %s: %w", vmName, derr)
	}
	return nil
}

// teardownOnFail tears down vmName via teardownVM and wraps err/msg into
// the (int, error) shape RunShell/provisionAndAttach return. Any
// cold-start-style failure (pre-service-start) must tear down the VM to
// avoid leaving a zombie — `devm shell` promises loud-failure: exit
// non-zero AND leave no half-created VM behind (pinned by test_51).
func (d ShellDeps) teardownOnFail(ctx context.Context, cfg schema.Config, vmName string, err error, msg string) (int, error) {
	debuglog.Logf("shell", "failed: %s: %v", msg, err)
	fmt.Fprintf(os.Stderr, "teardown-on-fail: %s: %v\n", msg, err)
	if terr := d.teardownVM(ctx, cfg, vmName); terr != nil {
		fmt.Fprintf(os.Stderr, "teardown-on-fail: %v\n", terr)
	}
	return -1, fmt.Errorf("%s: %w", msg, err)
}

// attachShell attaches an interactive shell inside the VM via `tart exec`.
// The tart binary is invoked via UserSpawner so the user's terminal
// stdin/stdout/stderr are inherited (ExecSpawner with Interactive=true).
//
// `tart exec` defaults to non-interactive: no stdin attached, no PTY
// allocated. When the caller's stdin is itself a TTY (a real terminal
// or pexpect), pass `-i -t` so bash sees a TTY and stays interactive
// instead of exiting on EOF.
//
// The user command is invoked via the with-devm-env wrapper so the
// project env (/opt/devm/.env) is sourced before argv runs. The wrapper
// lives in the guest at devmbundle.GuestWrapper, installed by the
// provisioner's "install devm bundle" step.
func (d ShellDeps) attachShell(ctx context.Context, vmName, repoRoot, cmdName string, cmdArgs []string) (int, error) {
	execArgs := []string{"exec"}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		execArgs = append(execArgs, "-i", "-t")
	}
	wrapper := devmbundle.GuestWrapper
	execArgs = append(execArgs, vmName)
	// Forward host terminal env into the guest so TUIs see the real
	// TERM (colors, keybindings, TUI capabilities). tart exec has no
	// --env flag, so we chain through env(1) inside the argv. Same
	// semantic the old sbx `-e KEY=VAL` block had; the tart migration
	// (c97bcc2) dropped it and colors regressed.
	execArgs = append(execArgs, terminalEnvForward()...)
	execArgs = append(execArgs, wrapper, cmdName)
	execArgs = append(execArgs, cmdArgs...)
	debuglog.Logf("shell", "attaching interactive shell: tart exec %s %v", vmName, execArgs)
	cmd, err := d.UserSpawner.Start(d.Tart.Path, execArgs...)
	if err != nil {
		return -1, fmt.Errorf("spawn interactive shell: %w", err)
	}
	rc, err := cmd.Wait()
	if err != nil {
		return -1, fmt.Errorf("interactive shell wait: %w", err)
	}
	return rc, nil
}

// waitVMReady polls `tart exec <vmName> true` until exit 0 or timeout.
// Each attempt is bounded by perAttemptTimeout so a single hung
// `tart exec` call (which can happen under system load when the guest
// agent socket is slow to respond) doesn't consume the whole budget
// and drop the effective retry count. Without this bound, we've seen
// 60s deadlines silently used up by 3-4 slow attempts instead of ~60.
func waitVMReady(ctx context.Context, tr *tart.Tart, vmName string, timeout time.Duration) error {
	const perAttemptTimeout = 3 * time.Second
	deadline := time.Now().Add(timeout)
	attempt := 0
	for time.Now().Before(deadline) {
		attempt++
		attemptCtx, cancel := context.WithTimeout(ctx, perAttemptTimeout)
		r := tr.Exec(attemptCtx, vmName, []string{"true"})
		cancel()
		if r.ExitCode == 0 {
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

// caStorageDir returns the ca/ directory alongside the socket,
// consistent with Ship 3's CA location. Follows the daemon-side
// $DEVM_RUNTIME_DIR override so an e2e-sandboxed CLI reads the CA
// from the same isolated dir the sandboxed daemon writes to.
func caStorageDir() string {
	return filepath.Join(serviceapi.RuntimeDir(), "ca")
}
