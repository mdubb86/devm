package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mdubb86/devm/internal/debuglog"
	"github.com/mdubb86/devm/internal/lock"
	"github.com/mdubb86/devm/internal/provision"
	"github.com/mdubb86/devm/internal/render"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/mdubb86/devm/internal/status"
)

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
		return d.attachShell(ctx, vmName, cmdName, cmdArgs)
	}

	// Cold start.
	reporter.Step("starting vm", false)
	debuglog.Logf("shell", "cold-start: sending StartVM to daemon")
	if err := d.ServiceAPIClient.StartVM(ctx, serviceapi.VMStartRequest{
		ProjectID:         cfg.Project.ID,
		VMName:            vmName,
		WorkspaceHostPath: repoRoot,
	}); err != nil {
		return -1, fmt.Errorf("start vm: %w", err)
	}

	// Wait for VM to accept exec connections.
	reporter.Step("waiting for vm ready", false)
	if err := waitVMReady(ctx, d.Tart, vmName, 60*time.Second); err != nil {
		return -1, fmt.Errorf("vm did not become ready: %w", err)
	}
	debuglog.Logf("shell", "cold-start: vm exec-ready")

	// Provision: CA, Caddyfile, dnsmasq, packages, install, services.
	reporter.Step("provisioning", false)
	caPEM, err := os.ReadFile(filepath.Join(caStorageDir(), "root.crt"))
	if err != nil {
		return -1, fmt.Errorf("read CA root: %w", err)
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
		return -1, fmt.Errorf("provision: %w", err)
	}
	debuglog.Logf("shell", "cold-start: provisioning done")

	reporter.Step("ready", false)
	reporter.Stop()
	reporter.Clear()

	_ = lk.Release()
	released = true

	return d.attachShell(ctx, vmName, cmdName, cmdArgs)
}

// attachShell attaches an interactive shell inside the VM via `tart exec`.
// The tart binary is invoked via UserSpawner so the user's terminal
// stdin/stdout/stderr are inherited (ExecSpawner with Interactive=true).
func (d ShellDeps) attachShell(ctx context.Context, vmName, cmdName string, cmdArgs []string) (int, error) {
	// argv: tart exec <vmName> <cmdName> [cmdArgs...]
	execArgs := append([]string{"exec", vmName, cmdName}, cmdArgs...)
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
