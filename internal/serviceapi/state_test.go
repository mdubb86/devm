package serviceapi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteRead_RoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := schema.Config{
		Project: schema.Project{ID: "myproj", VMName: "myproj-vm"},
	}
	require.NoError(t, WriteStateCfg("myproj", cfg))
	got, err := ReadStateCfg("myproj")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, cfg.Project.ID, got.Project.ID)
	assert.Equal(t, cfg.Project.VMName, got.Project.VMName)
}

func TestReadStateCfg_Missing_ReturnsNilNil(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got, err := ReadStateCfg("absent")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestReadStateCfg_Malformed_ReturnsNilNil(t *testing.T) {
	// Malformed snapshot is treated as missing so reconcile falls
	// back to a full diff. Callers that log this get to notice; the
	// hot path stays reliable.
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, os.MkdirAll(StateDir(), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(StateDir(), "junk.json"), []byte("{oh no"), 0o600))
	got, err := ReadStateCfg("junk")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestRemoveStateCfg_Idempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Remove of missing is not an error.
	require.NoError(t, RemoveStateCfg("nope"))
	require.NoError(t, WriteStateCfg("x", schema.Config{Project: schema.Project{ID: "x", VMName: "x-vm"}}))
	require.NoError(t, RemoveStateCfg("x"))
	got, err := ReadStateCfg("x")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestWriteStateCfg_Atomic(t *testing.T) {
	// A concurrent reader must never see a partial write. We assert
	// on the mechanism (temp file + rename) rather than the race
	// itself: after WriteStateCfg returns, no .tmp file lingers.
	t.Setenv("HOME", t.TempDir())
	cfg := schema.Config{Project: schema.Project{ID: "p", VMName: "p-vm"}}
	require.NoError(t, WriteStateCfg("p", cfg))
	entries, err := os.ReadDir(StateDir())
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, filepath.Ext(e.Name()) == ".tmp",
			"leftover temp file: %s", e.Name())
	}
}
