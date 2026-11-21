package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunAttach_StoppedVM_RefusesAndNamesDevmStart proves `devm shell`
// never cold-starts: with the VM reported stopped, RunAttach must
// refuse and point the user at `devm start` instead of calling
// StartVM.
func TestRunAttach_StoppedVM_RefusesAndNamesDevmStart(t *testing.T) {
	repoRoot := t.TempDir()
	admin := &fakeVMAdmin{
		statusResp: serviceapi.VMStatusResponse{Present: true, Running: false},
	}
	// tart is stubbed to always succeed; a stopped VM must short-circuit
	// on the daemon's VMStatus alone and never even probe it.
	deps := ShellDeps{
		Ident:            identity.Prod,
		Tart:             fakeTartBin(t, repoRoot),
		ServiceAPIClient: admin,
	}

	var stderr bytes.Buffer
	rc, err := RunAttach(context.Background(), deps, "x-sbx", repoRoot, "bash", nil, &stderr)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSandboxNotRunning))
	assert.Equal(t, -1, rc)
	assert.Contains(t, stderr.String(), "sandbox not running")
	assert.Contains(t, stderr.String(), "devm start")

	admin.mu.Lock()
	assert.Equal(t, 0, admin.startCalled, "RunAttach must never call StartVM")
	admin.mu.Unlock()
}

// TestRunAttach_RunningNotProvisioned_RefusesAndNamesDevmStart proves
// that a VM process that's up but hasn't finished provisioning
// (devm.target not active) is refused the same way a stopped VM is —
// `devm shell` never adopts/provisions in place; that's `devm start`'s
// job.
func TestRunAttach_RunningNotProvisioned_RefusesAndNamesDevmStart(t *testing.T) {
	repoRoot := t.TempDir()
	admin := &fakeVMAdmin{
		statusResp: serviceapi.VMStatusResponse{Present: true, Running: true},
	}
	tartBin, _ := fakeTartBinState(t, repoRoot, false /* devm.target inactive */, false)
	deps := ShellDeps{
		Ident:            identity.Prod,
		Tart:             tartBin,
		ServiceAPIClient: admin,
	}

	var stderr bytes.Buffer
	rc, err := RunAttach(context.Background(), deps, "x-sbx", repoRoot, "bash", nil, &stderr)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSandboxNotProvisioned))
	assert.Equal(t, -1, rc)
	assert.Contains(t, stderr.String(), "not yet provisioned")
	assert.Contains(t, stderr.String(), "devm start")

	admin.mu.Lock()
	assert.Equal(t, 0, admin.startCalled, "RunAttach must never call StartVM")
	assert.Equal(t, 0, admin.applyIronProxyCalled, "RunAttach must never adopt-in-place")
	admin.mu.Unlock()
}

// TestRunAttach_RunningAndProvisioned_WarmAttaches proves the happy
// path: a running, provisioned VM gets a warm attach with no StartVM
// call, matching RunShell's own warm path.
func TestRunAttach_RunningAndProvisioned_WarmAttaches(t *testing.T) {
	repoRoot := t.TempDir()
	admin := &fakeVMAdmin{
		statusResp: serviceapi.VMStatusResponse{Present: true, Running: true},
	}
	tartBin, logPath := fakeTartBinState(t, repoRoot, true /* devm.target active */, false)

	userCmd := &stubCmd{waitErr: make(chan error, 1)}
	userCmd.waitErr <- nil
	spawner := &stubSpawner{cmdQueue: []*stubCmd{userCmd}}

	deps := ShellDeps{
		Ident:            identity.Prod,
		Tart:             tartBin,
		ServiceAPIClient: admin,
		UserSpawner:      spawner,
	}

	var stderr bytes.Buffer
	rc, err := RunAttach(context.Background(), deps, "x-sbx", repoRoot, "bash", nil, &stderr)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)

	admin.mu.Lock()
	assert.Equal(t, 0, admin.startCalled, "StartVM must NOT be called on the warm-attach path")
	assert.Equal(t, 1, admin.endProvisioningCalled,
		"warm attach must re-assert ENFORCED before attaching (belt-and-suspenders)")
	admin.mu.Unlock()

	logBytes, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(logBytes), "is-active devm.target")
}
