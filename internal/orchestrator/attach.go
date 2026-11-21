package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/mdubb86/devm/internal/status"
)

// ErrSandboxNotRunning is returned by RunAttach when the project's VM
// isn't running. Distinguished from other RunAttach failures so the
// CLI never needs to string-match stderr to tell "not running" apart
// from "not provisioned" or a transport error.
var ErrSandboxNotRunning = errors.New("sandbox not running")

// ErrSandboxNotProvisioned is returned by RunAttach when the VM is
// running but hasn't finished provisioning (devm.target not active).
var ErrSandboxNotProvisioned = errors.New("sandbox not provisioned")

// RunAttach implements `devm shell`'s warm-attach-only semantics: it
// never cold-starts and never adopts a dirty/unprovisioned VM in
// place. `devm start` owns both of those — the sole command that
// reads devm.yaml for apply and the sole command that can hit the
// approve gate's 409. If the sandbox is stopped or hasn't finished
// provisioning, RunAttach prints a clear stderr message naming
// `devm start` and returns without touching StartVM.
//
// Mirrors RunShell's own "is this VM provisioned" probe (devm.target
// active, via ExecWithRetry) rather than anything from VMStatus — the
// daemon's /vm/status only tracks whether the VM process is up, not
// whether provisioning inside the guest ever finished.
func RunAttach(ctx context.Context, d ShellDeps, vmName, repoRoot, cmdName string, cmdArgs []string, stderr io.Writer) (int, error) {
	vmStatus, err := d.ServiceAPIClient.VMStatus(ctx, vmName)
	if err != nil {
		return -1, fmt.Errorf("query vm status: %w", err)
	}
	if !vmStatus.Running {
		fmt.Fprintln(stderr, "sandbox not running; run `devm start` first.")
		return -1, ErrSandboxNotRunning
	}

	provisioned := d.Tart.ExecWithRetry(ctx, vmName,
		[]string{"systemctl", "is-active", "devm.target"}).ExitCode == 0
	if !provisioned {
		fmt.Fprintln(stderr, "sandbox not yet provisioned; run `devm start` to finish provisioning.")
		return -1, ErrSandboxNotProvisioned
	}

	reporter := status.New(stderr)
	defer reporter.Stop()
	return d.warmAttach(ctx, vmName, repoRoot, cmdName, cmdArgs, reporter)
}
