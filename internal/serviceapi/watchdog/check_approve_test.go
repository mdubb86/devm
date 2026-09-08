package watchdog

import (
	"context"
	"testing"
	"time"

	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApproveCheck_NoDrift_TouchesReconciled(t *testing.T) {
	since := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	cache := serviceapi.NewStateCache()
	cache.SetMacCwd("p", "/mac/p")
	cache.SetApproveState("p", serviceapi.ApproveStateSummary{
		Diverged:        false,
		CurrentDevmSHA:  "a",
		ApprovedDevmSHA: "a",
		CurrentMeSHA:    "b",
		ApprovedMeSHA:   "b",
		ApprovedSince:   &since,
	})
	fake := &fakeGroundTruth{
		Projects: []string{"p"},
		ApproveHashFn: func(macCwd string) (string, string, error) {
			assert.Equal(t, "/mac/p", macCwd)
			return "a", "b", nil
		},
		ReadSnapshotFn: func(projectID string) (string, string, *time.Time, bool, error) {
			assert.Equal(t, "p", projectID)
			return "a", "b", &since, true, nil
		},
	}
	check := NewApproveCheck()
	drifted, err := check.Run(context.Background(), cache, fake)
	require.NoError(t, err)
	assert.False(t, drifted)

	row, _ := cache.ProjectRow("p")
	assert.False(t, row.LastReconciledAt.IsZero())
	assert.False(t, row.ApproveState.Diverged)
}

func TestApproveCheck_CurrentFileChanged_CacheReconciles(t *testing.T) {
	cache := serviceapi.NewStateCache()
	cache.SetMacCwd("p", "/mac/p")
	cache.SetApproveState("p", serviceapi.ApproveStateSummary{
		Diverged:        false,
		CurrentDevmSHA:  "a",
		ApprovedDevmSHA: "a",
	})
	fake := &fakeGroundTruth{
		Projects: []string{"p"},
		ApproveHashFn: func(macCwd string) (string, string, error) {
			// devm.yaml edited on disk since the cache last saw it.
			return "b", "", nil
		},
		ReadSnapshotFn: func(projectID string) (string, string, *time.Time, bool, error) {
			return "a", "", nil, true, nil
		},
	}
	check := NewApproveCheck()
	drifted, err := check.Run(context.Background(), cache, fake)
	require.NoError(t, err)
	assert.True(t, drifted)

	row, _ := cache.ProjectRow("p")
	assert.True(t, row.ApproveState.Diverged)
	assert.Equal(t, "b", row.ApproveState.CurrentDevmSHA)
	assert.Equal(t, "a", row.ApproveState.ApprovedDevmSHA)
	assert.False(t, row.LastReconciledAt.IsZero())
}

func TestApproveCheck_EmptyMacCwd_Skipped(t *testing.T) {
	cache := serviceapi.NewStateCache()
	// No SetMacCwd call — MacCwd stays "" (project not started yet).
	cache.SetApproveState("p", serviceapi.ApproveStateSummary{Diverged: true, CurrentDevmSHA: "x"})
	fake := &fakeGroundTruth{
		Projects: []string{"p"},
		ApproveHashFn: func(macCwd string) (string, string, error) {
			t.Fatalf("ApproveHash must not be called when mac_cwd is empty")
			return "", "", nil
		},
		ReadSnapshotFn: func(projectID string) (string, string, *time.Time, bool, error) {
			t.Fatalf("ReadApprovedSnapshot must not be called when mac_cwd is empty")
			return "", "", nil, false, nil
		},
	}
	check := NewApproveCheck()
	drifted, err := check.Run(context.Background(), cache, fake)
	require.NoError(t, err)
	assert.False(t, drifted)

	row, _ := cache.ProjectRow("p")
	assert.True(t, row.LastReconciledAt.IsZero(), "cache must not be touched for a project that hasn't started")
	assert.Equal(t, "x", row.ApproveState.CurrentDevmSHA, "cache must not be mutated for a project that hasn't started")
}
