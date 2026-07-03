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
	"github.com/mdubb86/devm/internal/lock"
	"github.com/mdubb86/devm/internal/provision"
	"github.com/mdubb86/devm/internal/render"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/secret"
	"github.com/mdubb86/devm/internal/serviceapi"
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
	LockPath         string
	// UserSpawner runs the interactive shell command. Production code
	// uses ExecSpawner; tests use a stub.
	UserSpawner Spawner
}

// VMAdminClient is the subset of serviceapi.Client used by RunShell.
// Extracted as an interface so tests can inject a fake.
type VMAdminClient interface {
	VMStatus(ctx context.Context, projectID, vmName string) (serviceapi.VMStatusResponse, error)
	StartVM(ctx context.Context, req serviceapi.VMStartRequest) error
	StopVM(ctx context.Context, projectID, vmName string) error
}

// DefaultShellDeps returns deps wired for production.
func DefaultShellDeps(repoRoot string) ShellDeps {
	return ShellDeps{
		Tart:             tart.New(),
		ServiceAPIClient: serviceapi.NewClient(),
		UserSpawner:      &ExecSpawner{Interactive: true},
		LockPath:         filepath.Join(repoRoot, ".devm", "lock"),
	}
}

// RunShell implements `devm shell`. Returns the user shell's exit code
// and a non-nil error only when an orchestration step itself failed.
func RunShell(ctx context.Context, d ShellDeps, cfg schema.Config, repoRoot, vmName, cmdName string, cmdArgs []string) (int, error) {
	reporter := status.New(os.Stderr)
	defer reporter.Stop()
	reporter.Start("starting up")

	lk, err := lock.Acquire(d.LockPath)
	if err != nil {
		return -1, fmt.Errorf("acquire lock: %w", err)
	}
	released := false
	defer func() {
		if !released {
			_ = lk.Release()
		}
	}()

	// Render .devm/ from the current devm.yaml before doing anything.
	// Cheap and idempotent; ensures cold-start picks up current config.
	if err := render.WriteDevmDir(cfg, repoRoot); err != nil {
		return -1, fmt.Errorf("render devm dir: %w", err)
	}

	// Check VM state via daemon admin.
	vmStatus, err := d.ServiceAPIClient.VMStatus(ctx, cfg.Project.ID, vmName)
	if err != nil {
		return -1, fmt.Errorf("query vm status: %w", err)
	}
	debuglog.Logf("shell", "vm status: present=%v running=%v", vmStatus.Present, vmStatus.Running)

	if vmStatus.Running {
		// Warm attach: VM is already up. Auto-apply LIVE changes before
		// attaching. We already hold the lock, so use the lock-less inner.
		reporter.Step("attaching to running vm", false)
		// Warm attach: reconcile is handled by the provisioner on cold start.
		// For now the warm path just attaches directly.
		_ = lk.Release()
		released = true
		reporter.Step("ready", false)
		reporter.Stop()
		reporter.Clear()
		return d.attachShell(ctx, vmName, repoRoot, cmdName, cmdArgs)
	}

	// Cold start.
	reporter.Step("starting vm", false)
	debuglog.Logf("shell", "cold-start: sending StartVM to daemon")

	// Collect allow-list from network config.
	allowList := cfg.Network.Domains()

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

	if err := d.ServiceAPIClient.StartVM(ctx, serviceapi.VMStartRequest{
		ProjectID:         cfg.Project.ID,
		VMName:            vmName,
		WorkspaceHostPath: repoRoot,
		AllowList:         allowList,
		Secrets:           bindings,
		ExtraMounts:       extraMounts,
	}); err != nil {
		return -1, fmt.Errorf("start vm: %w", err)
	}

	// From here on, any cold-start failure must tear down the VM to avoid
	// leaving a zombie. `devm shell` promises loud-failure: exit non-zero
	// AND leave no half-created VM behind (pinned by test_51).
	teardownOnFail := func(err error, msg string) (int, error) {
		debuglog.Logf("shell", "cold-start failed after StartVM: %s: %v", msg, err)
		teardownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = d.ServiceAPIClient.StopVM(teardownCtx, cfg.Project.ID, vmName)
		if derr := d.Tart.Delete(teardownCtx, vmName); derr != nil &&
			!strings.Contains(derr.Error(), "does not exist") {
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
	prov := &provision.Provisioner{
		Tart:            d.Tart,
		VMName:          vmName,
		Cfg:             cfg,
		CARootPEM:       caPEM,
		WorkspaceVMPath: repoRoot,
	}
	debuglog.Logf("shell", "cold-start: provisioning")
	if err := prov.Run(ctx, os.Stdout); err != nil {
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

	reporter.Step("ready", false)
	reporter.Stop()
	reporter.Clear()

	_ = lk.Release()
	released = true

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
// project env (.devm/.env) is sourced before argv runs. The wrapper
// lives at $WORKSPACE/.devm/scripts/with-devm-env.sh on the host and
// surfaces at the same absolute path inside the VM via the workspace
// virtiofs share.
func (d ShellDeps) attachShell(ctx context.Context, vmName, repoRoot, cmdName string, cmdArgs []string) (int, error) {
	execArgs := []string{"exec"}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		execArgs = append(execArgs, "-i", "-t")
	}
	wrapper := filepath.Join(repoRoot, ".devm", "scripts", "with-devm-env")
	execArgs = append(execArgs, vmName, wrapper, cmdName)
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
func waitVMReady(ctx context.Context, tr *tart.Tart, vmName string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r := tr.Exec(ctx, vmName, []string{"true"})
		if r.ExitCode == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
	return fmt.Errorf("timeout waiting for vm %s to become exec-ready", vmName)
}

// caStorageDir returns ~/Library/Application Support/devm/ca/,
// consistent with Ship 3's CA location.
func caStorageDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "devm", "ca")
}
