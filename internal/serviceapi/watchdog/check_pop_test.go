package watchdog

import (
	"context"
	"testing"

	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPopCheck_NoDrift_TouchesReconciled(t *testing.T) {
	cache := serviceapi.NewStateCache()
	cache.SetPopSessionSummary("p", serviceapi.PopSessionSummary{Count: 2, OldestAgeSeconds: 42})
	fake := &fakeGroundTruth{
		Projects: []string{"p"},
		PopSummaryFn: func(projectID string) serviceapi.PopSessionSummary {
			return serviceapi.PopSessionSummary{Count: 2, OldestAgeSeconds: 42}
		},
	}
	check := NewPopCheck()
	drifted, err := check.Run(context.Background(), cache, fake)
	require.NoError(t, err)
	assert.False(t, drifted)
	row, _ := cache.ProjectRow("p")
	assert.False(t, row.LastReconciledAt.IsZero())
}

func TestPopCheck_Drift_CacheReconciles(t *testing.T) {
	cache := serviceapi.NewStateCache()
	cache.SetPopSessionSummary("p", serviceapi.PopSessionSummary{Count: 0, OldestAgeSeconds: 0})
	fake := &fakeGroundTruth{
		Projects: []string{"p"},
		PopSummaryFn: func(projectID string) serviceapi.PopSessionSummary {
			return serviceapi.PopSessionSummary{Count: 3, OldestAgeSeconds: 100}
		},
	}
	check := NewPopCheck()
	drifted, err := check.Run(context.Background(), cache, fake)
	require.NoError(t, err)
	assert.True(t, drifted)
	row, _ := cache.ProjectRow("p")
	assert.Equal(t, serviceapi.PopSessionSummary{Count: 3, OldestAgeSeconds: 100}, row.PopSessions)
	assert.False(t, row.LastReconciledAt.IsZero())
}
