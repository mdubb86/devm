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
