package serviceapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi/sshkeys"
)

// erroringOrphanGCTart always fails List, for the fail-safe test.
type erroringOrphanGCTart struct{}

func (erroringOrphanGCTart) List(context.Context) ([]tart.VM, error) {
	return nil, errors.New("tart: boom")
}

func TestGCOrphanedProjects_RemovesStateForMissingVM(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	require.NoError(t, WriteStateSnapshot(identity.Prod, "kept", StateSnapshot{Cfg: schema.Config{}}))
	require.NoError(t, WriteStateSnapshot(identity.Prod, "orphaned", StateSnapshot{Cfg: schema.Config{}}))

	// Seed sidecar artifacts for the orphaned project — everything
	// GCOrphanedProjects is documented to clean up alongside the state
	// file.
	ironCfgPath, err := IronProxyConfigPath(identity.Prod, "orphaned")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(ironCfgPath), 0o755))
	require.NoError(t, os.WriteFile(ironCfgPath, []byte("stub\n"), 0o600))

	projectDir := filepath.Join(identity.Prod.RuntimeDir(), "orphaned")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	pendingPath := filepath.Join(projectDir, "pending-passthrough.json")
	require.NoError(t, os.WriteFile(pendingPath, []byte("{}"), 0o600))
	lastProposalPath := filepath.Join(projectDir, "last-proposal.json")
	require.NoError(t, os.WriteFile(lastProposalPath, []byte("{}"), 0o600))
	approvedSnapshotDir := filepath.Join(projectDir, "approved-snapshot")
	require.NoError(t, os.MkdirAll(approvedSnapshotDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(approvedSnapshotDir, "devm.yaml"), []byte("x"), 0o600))

	_, err = sshkeys.EnsureProjectKeypair(identity.Prod, "orphaned")
	require.NoError(t, err)
	sshDir := sshkeys.ProjectDir(identity.Prod, "orphaned")
	require.DirExists(t, sshDir)

	tr := staticTartList{vms: []tart.VM{
		{Name: "kept", Running: true},
	}}

	require.NoError(t, GCOrphanedProjects(context.Background(), identity.Prod, tr))

	// Kept project: everything survives.
	keptSnap, err := ReadStateSnapshot(identity.Prod, "kept")
	require.NoError(t, err)
	assert.NotNil(t, keptSnap)

	// Orphaned project: state file and every sidecar are gone.
	orphanedSnap, err := ReadStateSnapshot(identity.Prod, "orphaned")
	require.NoError(t, err)
	assert.Nil(t, orphanedSnap)
	assert.NoFileExists(t, ironCfgPath)
	assert.NoFileExists(t, pendingPath)
	assert.NoFileExists(t, lastProposalPath)
	assert.NoDirExists(t, approvedSnapshotDir)
	assert.NoDirExists(t, sshDir)
}

// TestGCOrphanedProjects_RemovesSoftnetSocketAndLogFiles pins the fix
// for I5: the pre-fix GC left the per-project softnet control socket
// AND every log file under <LogDir>/<projectID>-*.log behind, causing
// a slow accumulation of orphan artifacts across e2e sweeps and
// user-facing project churn. Both are removed now; this test would
// fail if either removal is dropped.
func TestGCOrphanedProjects_RemovesSoftnetSocketAndLogFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	require.NoError(t, WriteStateSnapshot(identity.Prod, "orphaned", StateSnapshot{Cfg: schema.Config{}}))

	// Softnet control socket file (real path is a hash under a
	// per-user tmp dir; we create the parent + a stub file at the
	// deterministic path so the removal can be observed).
	softnetSock := SoftnetControlSock(identity.Prod, "orphaned")
	require.NoError(t, os.MkdirAll(filepath.Dir(softnetSock), 0o755))
	require.NoError(t, os.WriteFile(softnetSock, []byte("sock-stub"), 0o600))

	// Log files: <LogDir>/<projectID>-<role>.log for a couple of roles.
	require.NoError(t, os.MkdirAll(identity.Prod.LogDir(), 0o755))
	orphanedIronLog := filepath.Join(identity.Prod.LogDir(), "orphaned-iron-proxy.log")
	orphanedSoftnetLog := filepath.Join(identity.Prod.LogDir(), "orphaned-softnet.log")
	require.NoError(t, os.WriteFile(orphanedIronLog, []byte("iron\n"), 0o644))
	require.NoError(t, os.WriteFile(orphanedSoftnetLog, []byte("softnet\n"), 0o644))
	// Unrelated log from a different project — must survive.
	require.NoError(t, os.WriteFile(filepath.Join(identity.Prod.LogDir(), "kept-iron-proxy.log"), []byte("keep\n"), 0o644))
	// A log file whose name starts with the orphaned prefix but doesn't
	// end in .log — must survive (glob restrictor).
	require.NoError(t, os.WriteFile(filepath.Join(identity.Prod.LogDir(), "orphaned-iron-proxy.log.1"), []byte("rot\n"), 0o644))

	tr := staticTartList{vms: []tart.VM{}}
	require.NoError(t, GCOrphanedProjects(context.Background(), identity.Prod, tr))

	assert.NoFileExists(t, softnetSock,
		"softnet control socket for the orphaned project must be removed")
	assert.NoFileExists(t, orphanedIronLog,
		"orphaned project's iron-proxy log must be removed")
	assert.NoFileExists(t, orphanedSoftnetLog,
		"orphaned project's softnet log must be removed")
	assert.FileExists(t, filepath.Join(identity.Prod.LogDir(), "kept-iron-proxy.log"),
		"unrelated project's logs must survive")
	assert.FileExists(t, filepath.Join(identity.Prod.LogDir(), "orphaned-iron-proxy.log.1"),
		"non-.log suffixes must survive — the glob is scoped to .log only")
}

func TestGCOrphanedProjects_LeavesLiveStateAloneWhenTartListFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	require.NoError(t, WriteStateSnapshot(identity.Prod, "proj-a", StateSnapshot{Cfg: schema.Config{}}))

	err := GCOrphanedProjects(context.Background(), identity.Prod, erroringOrphanGCTart{})
	require.Error(t, err)

	snap, err := ReadStateSnapshot(identity.Prod, "proj-a")
	require.NoError(t, err)
	assert.NotNil(t, snap, "state file must survive a tart list failure — fail-safe")
}

func TestGCOrphanedProjects_HandlesEmptyStateDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	tr := staticTartList{vms: []tart.VM{{Name: "devm-base", Running: true}}}
	require.NoError(t, GCOrphanedProjects(context.Background(), identity.Prod, tr))
}

func TestGCOrphanedProjects_LogsEachRemoval(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	require.NoError(t, WriteStateSnapshot(identity.Prod, "ghost-1", StateSnapshot{Cfg: schema.Config{}}))
	require.NoError(t, WriteStateSnapshot(identity.Prod, "ghost-2", StateSnapshot{Cfg: schema.Config{}}))
	require.NoError(t, WriteStateSnapshot(identity.Prod, "alive", StateSnapshot{Cfg: schema.Config{}}))

	tr := staticTartList{vms: []tart.VM{{Name: "alive", Running: true}}}

	buf := captureStdlibLog(t)
	require.NoError(t, GCOrphanedProjects(context.Background(), identity.Prod, tr))

	logged := buf.String()
	assert.Contains(t, logged, `orphan-gc: removed stale project "ghost-1"`)
	assert.Contains(t, logged, `orphan-gc: removed stale project "ghost-2"`)
	assert.NotContains(t, logged, `project "alive"`)
}
