package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mdubb86/devm/internal/debuglog"
	"github.com/mdubb86/devm/internal/devmbundle"
	"github.com/mdubb86/devm/internal/docker"
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
		v, err := backend.Get(cfg.Project.ID + "/" + n)
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
	VMStatus(ctx context.Context, projectID, vmName string) (serviceapi.VMStatusResponse, error)
	StartVM(ctx context.Context, req serviceapi.VMStartRequest) error
	ApplyEgressEnforcement(ctx context.Context, projectID, vmName string) error
	StopVM(ctx context.Context, projectID, vmName string) error
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
	vmStatus, err := d.ServiceAPIClient.VMStatus(ctx, cfg.Project.ID, vmName)
	if err != nil {
		return -1, fmt.Errorf("query vm status: %w", err)
	}
	debuglog.Logf("shell", "vm status: present=%v running=%v", vmStatus.Present, vmStatus.Running)

	if vmStatus.Running {
		// Warm attach: VM is already up. Auto-apply LIVE changes before
		// attaching.
		reporter.Step("attaching to running vm", false)
		// Warm attach: reconcile is handled by the provisioner on cold start.
		// For now the warm path just attaches directly.
		reporter.Step("ready", false)
		reporter.Stop()
		reporter.Clear()
		return d.attachShell(ctx, vmName, repoRoot, cmdName, cmdArgs)
	}

	// Cold start.
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
		ProjectID:         cfg.Project.ID,
		VMName:            vmName,
		WorkspaceHostPath: repoRoot,
		AllowList:         allowList,
		Secrets:           bindings,
		ExtraMounts:       extraMounts,
		DiskSizeGB:        diskGB,
	}); err != nil {
		return -1, fmt.Errorf("start vm: %w", err)
	}

	// From here on, any cold-start failure must tear down the VM to avoid
	// leaving a zombie. `devm shell` promises loud-failure: exit non-zero
	// AND leave no half-created VM behind (pinned by test_51).
	teardownOnFail := func(err error, msg string) (int, error) {
		debuglog.Logf("shell", "cold-start failed after StartVM: %s: %v", msg, err)
		fmt.Fprintf(os.Stderr, "teardown-on-fail: %s: %v\n", msg, err)

		teardownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if stopErr := d.ServiceAPIClient.StopVM(teardownCtx, cfg.Project.ID, vmName); stopErr != nil {
			// StopVM is best-effort here (VM may be already stopped or
			// gone), but if it errored for a reason worth diagnosing, we
			// want the caller to see it — otherwise this class of failure
			// (VM stopped but not deleted) is invisible from the outside.
			fmt.Fprintf(os.Stderr, "teardown-on-fail: StopVM: %v\n", stopErr)
			debuglog.Logf("shell", "teardown-on-fail: StopVM: %v", stopErr)
		}
		if derr := d.Tart.Delete(teardownCtx, vmName); derr != nil &&
			!strings.Contains(derr.Error(), "does not exist") {
			fmt.Fprintf(os.Stderr, "teardown-on-fail: tart delete %s failed: %v\n", vmName, derr)
			debuglog.Logf("shell", "teardown-on-fail: delete %s failed: %v", vmName, derr)
		}
		return -1, fmt.Errorf("%s: %w", msg, err)
	}

	// Wait for VM to accept exec connections.
	reporter.Step("waiting for vm ready", false)
	if err := waitVMReady(ctx, d.Tart, vmName, 60*time.Second); err != nil {
		return teardownOnFail(err, "vm did not become ready")
	}
	debuglog.Logf("shell", "cold-start: vm exec-ready")

	// Provision: CA, Caddyfile, dnsmasq, packages, install, services.
	reporter.Step("provisioning", false)
	caPEM, err := os.ReadFile(filepath.Join(caStorageDir(), "root.crt"))
	if err != nil {
		return teardownOnFail(err, "read CA root")
	}
	authPub, err := sshkeys.EnsureProjectKeypair(cfg.Project.ID)
	if err != nil {
		return teardownOnFail(err, "ensure project ssh keypair")
	}
	hostPriv, hostPub, err := sshkeys.EnsureProjectHostKey(cfg.Project.ID, cfg.Project.VMName)
	if err != nil {
		return teardownOnFail(err, "ensure project ssh host key")
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
		EnforceEgress: func(ctx context.Context) error {
			return d.ServiceAPIClient.ApplyEgressEnforcement(ctx, cfg.Project.ID, vmName)
		},
	}
	debuglog.Logf("shell", "cold-start: provisioning")
	// Provisioning output is DIAGNOSTIC — step names, package install
	// noise, etc. It belongs on stderr so `devm exec pwd` / `devm shell
	// -- <cmd>` produce clean stdout that scripts can pipe. Failure
	// details flow via the returned error, not via this writer.
	if err := prov.Run(ctx, os.Stderr); err != nil {
		// Service-phase failures (unit install, daemon-reload, enable+start,
		// apply masks) leave the VM in a debuggable state — user's fix is
		// in devm.yaml, not in the VM. Surface the error but keep the VM
		// alive so `tart exec <vm> systemctl status` etc. works. Pre-service
		// failures tear down (test_51: install failure = state=absent).
		if provision.IsPostInstallFailure(err) {
			debuglog.Logf("shell", "cold-start: post-install failure — keeping VM: %v", err)
			return -1, fmt.Errorf("provision: %w", err)
		}
		return teardownOnFail(err, "provision")
	}
	debuglog.Logf("shell", "cold-start: provisioning done")

	// Write initial snapshot so that subsequent `devm reconcile` calls have
	// a baseline to diff against. Without this, ReadSnapshot returns "" which
	// reconcile treats as zero-diff (identity with the new config), masking
	// any changes made between cold-start and the first reconcile.
	provSnap, err := yaml.Marshal(cfg)
	if err != nil {
		return teardownOnFail(err, "marshal provision snapshot")
	}
	if err := WriteSnapshot(d.Tart, vmName, snapshotHeader+string(provSnap)); err != nil {
		return teardownOnFail(err, "write provision snapshot")
	}
	debuglog.Logf("shell", "cold-start: snapshot written")

	// Seed the daemon-side state snapshot too, now that cold-start is
	// fully green (provisioning AND egress enforcement, which runs as
	// a step inside prov.Run, both succeeded). Without this, the first
	// `devm reconcile` after `devm start` finds no baseline, diffs
	// against schema.Config{}, and every teardown-bucket kind
	// spuriously surfaces as pending — prompting the user to tear down
	// the VM they just started. Best-effort: log but don't fail here —
	// a missing snapshot only degrades to "full diff on next
	// reconcile" (safe), and failing here would kill a cold start that
	// otherwise succeeded.
	templateContents, err := render.RenderTemplatesByBasename(cfg, repoRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "state: render templates for seed snapshot %s failed: %v\n", cfg.Project.ID, err)
	}
	snap := serviceapi.StateSnapshot{
		Cfg:              cfg,
		TemplateContents: templateContents,
		SecretHashes:     SecretHashesFromBindings(bindings),
	}
	if err := serviceapi.WriteStateSnapshot(cfg.Project.ID, snap); err != nil {
		fmt.Fprintf(os.Stderr, "state: seed snapshot for %s failed: %v\n", cfg.Project.ID, err)
	}

	reporter.Step("ready", false)
	reporter.Stop()
	reporter.Clear()

	return d.attachShell(ctx, vmName, repoRoot, cmdName, cmdArgs)
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
