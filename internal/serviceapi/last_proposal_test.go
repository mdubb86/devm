package serviceapi

import (
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLastProposal_WriteReadRoundtrip(t *testing.T) {
	cfg := identity.Prod
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	m := ProposalMetadata{
		Cwd:       "/home/devm/proj/backend",
		Branch:    "feature-postgres",
		Reason:    "added postgres client for the new schema",
		Timestamp: "2026-09-14T14:30:00Z",
		Source:    "guest",
		Kind:      "devm.yaml",
	}
	require.NoError(t, WriteLastProposal(cfg, "proj", m))

	got, ok, err := ReadLastProposal(cfg, "proj")
	require.NoError(t, err)
	require.True(t, ok, "must find just-written proposal")
	assert.Equal(t, m, *got)
}

func TestLastProposal_ReadAbsent(t *testing.T) {
	cfg := identity.Prod
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	got, ok, err := ReadLastProposal(cfg, "nonexistent")
	assert.NoError(t, err)
	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestLastProposal_ClearAbsentIsNoop(t *testing.T) {
	cfg := identity.Prod
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	assert.NoError(t, ClearLastProposal(cfg, "never-existed"))
}

func TestLastProposal_ClearRemovesFile(t *testing.T) {
	cfg := identity.Prod
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	m := ProposalMetadata{Cwd: "/x", Timestamp: "2026-09-14T00:00:00Z", Source: "guest", Kind: "devm.yaml"}
	require.NoError(t, WriteLastProposal(cfg, "proj", m))

	_, ok, _ := ReadLastProposal(cfg, "proj")
	require.True(t, ok)

	require.NoError(t, ClearLastProposal(cfg, "proj"))
	_, ok, _ = ReadLastProposal(cfg, "proj")
	assert.False(t, ok, "must be absent after Clear")
}

func TestLastProposal_WriteOverwrites(t *testing.T) {
	cfg := identity.Prod
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	first := ProposalMetadata{Cwd: "/first", Reason: "first", Timestamp: "2026-09-14T00:00:00Z", Source: "guest", Kind: "devm.yaml"}
	second := ProposalMetadata{Cwd: "/second", Reason: "second", Timestamp: "2026-09-14T00:00:05Z", Source: "guest", Kind: "devm.yaml"}
	require.NoError(t, WriteLastProposal(cfg, "proj", first))
	require.NoError(t, WriteLastProposal(cfg, "proj", second))

	got, ok, err := ReadLastProposal(cfg, "proj")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, second, *got)
}
