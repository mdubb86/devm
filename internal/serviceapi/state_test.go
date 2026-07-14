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
	require.NoError(t, WriteStateSnapshot("myproj", StateSnapshot{Cfg: cfg}))
	got, err := ReadStateSnapshot("myproj")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, cfg.Project.ID, got.Cfg.Project.ID)
	assert.Equal(t, cfg.Project.VMName, got.Cfg.Project.VMName)
}

func TestReadStateSnapshot_Missing_ReturnsNilNil(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got, err := ReadStateSnapshot("absent")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestReadStateSnapshot_Malformed_ReturnsNilNil(t *testing.T) {
	// Malformed snapshot is treated as missing so reconcile falls
	// back to a full diff. Callers that log this get to notice; the
	// hot path stays reliable.
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, os.MkdirAll(StateDir(), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(StateDir(), "junk.json"), []byte("{oh no"), 0o600))
	got, err := ReadStateSnapshot("junk")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestRemoveStateCfg_Idempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Remove of missing is not an error.
	require.NoError(t, RemoveStateCfg("nope"))
	require.NoError(t, WriteStateSnapshot("x", StateSnapshot{
		Cfg: schema.Config{Project: schema.Project{ID: "x", VMName: "x-vm"}},
	}))
	require.NoError(t, RemoveStateCfg("x"))
	got, err := ReadStateSnapshot("x")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestWriteStateSnapshot_Atomic(t *testing.T) {
	// A concurrent reader must never see a partial write. We assert
	// on the mechanism (temp file + rename) rather than the race
	// itself: after WriteStateSnapshot returns, no os.CreateTemp-style
	// temp file matching "<project-id>.json.*" lingers alongside
	// the final "<project-id>.json".
	t.Setenv("HOME", t.TempDir())
	cfg := schema.Config{Project: schema.Project{ID: "p", VMName: "p-vm"}}
	require.NoError(t, WriteStateSnapshot("p", StateSnapshot{Cfg: cfg}))

	entries, err := os.ReadDir(StateDir())
	require.NoError(t, err)
	require.Len(t, entries, 1, "expected exactly one file in state dir, got: %v", names(entries))
	assert.Equal(t, "p.json", entries[0].Name(),
		"only the final file should remain; os.CreateTemp temp files must have been renamed away")
}

func TestState_RejectsPathTraversal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, id := range []string{"../evil", "foo/bar", "..", "foo\\bar"} {
		t.Run(id, func(t *testing.T) {
			require.Error(t, WriteStateSnapshot(id, StateSnapshot{}))
			_, err := ReadStateSnapshot(id)
			require.Error(t, err)
			require.Error(t, RemoveStateCfg(id))
		})
	}
}

func TestStateSnapshot_SecretHashesRoundtrip(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", filepath.Join(t.TempDir(), "rd"))

	snap := StateSnapshot{
		Cfg:          schema.Config{Project: schema.Project{ID: "p", VMName: "p-vm"}},
		SecretHashes: map[string]string{"TOK": "abc123"},
	}
	require.NoError(t, WriteStateSnapshot("p", snap))

	read, err := ReadStateSnapshot("p")
	require.NoError(t, err)
	require.NotNil(t, read)
	assert.Equal(t, "abc123", read.SecretHashes["TOK"])
}

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}
