package serviceapi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteRead_RoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := schema.Config{
		Project: schema.Project{Name: "myproj"},
	}
	require.NoError(t, WriteStateSnapshot(identity.Prod, "myproj", StateSnapshot{Cfg: cfg}))
	got, err := ReadStateSnapshot(identity.Prod, "myproj")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, cfg.Project.Name, got.Cfg.Project.Name)
	assert.Equal(t, cfg.Project.Name, got.Cfg.Project.Name)
}

func TestReadStateSnapshot_Missing_ReturnsNilNil(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got, err := ReadStateSnapshot(identity.Prod, "absent")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestReadStateSnapshot_Malformed_ReturnsError(t *testing.T) {
	// A snapshot file that exists but fails to parse must fail loud —
	// the alternative (treating it as missing) silently loses every
	// adopted iron-proxy PID, project IP, and workspace path on every
	// reconcile for the lifetime of the daemon. The error carries the
	// file path so the operator can act on it directly.
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, os.MkdirAll(StateDir(identity.Prod), 0o700))
	junkPath := filepath.Join(StateDir(identity.Prod), "junk.json")
	require.NoError(t, os.WriteFile(junkPath, []byte("{oh no"), 0o600))
	got, err := ReadStateSnapshot(identity.Prod, "junk")
	require.Error(t, err)
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), junkPath,
		"error must name the malformed file so the operator can find and delete it")
}

func TestRemoveStateCfg_Idempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Remove of missing is not an error.
	require.NoError(t, RemoveStateCfg(identity.Prod, "nope"))
	require.NoError(t, WriteStateSnapshot(identity.Prod, "x", StateSnapshot{
		Cfg: schema.Config{Project: schema.Project{Name: "x"}},
	}))
	require.NoError(t, RemoveStateCfg(identity.Prod, "x"))
	got, err := ReadStateSnapshot(identity.Prod, "x")
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
	cfg := schema.Config{Project: schema.Project{Name: "p"}}
	require.NoError(t, WriteStateSnapshot(identity.Prod, "p", StateSnapshot{Cfg: cfg}))

	entries, err := os.ReadDir(StateDir(identity.Prod))
	require.NoError(t, err)
	require.Len(t, entries, 1, "expected exactly one file in state dir, got: %v", names(entries))
	assert.Equal(t, "p.json", entries[0].Name(),
		"only the final file should remain; os.CreateTemp temp files must have been renamed away")
}

func TestState_RejectsPathTraversal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, id := range []string{"../evil", "foo/bar", "..", "foo\\bar"} {
		t.Run(id, func(t *testing.T) {
			require.Error(t, WriteStateSnapshot(identity.Prod, id, StateSnapshot{}))
			_, err := ReadStateSnapshot(identity.Prod, id)
			require.Error(t, err)
			require.Error(t, RemoveStateCfg(identity.Prod, id))
		})
	}
}

func TestStateSnapshot_SecretHashesRoundtrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	snap := StateSnapshot{
		Cfg:          schema.Config{Project: schema.Project{Name: "p"}},
		SecretHashes: map[string]string{"TOK": "abc123"},
	}
	require.NoError(t, WriteStateSnapshot(identity.Prod, "p", snap))

	read, err := ReadStateSnapshot(identity.Prod, "p")
	require.NoError(t, err)
	require.NotNil(t, read)
	assert.Equal(t, "abc123", read.SecretHashes["TOK"])
}

func TestStateSnapshotProxyVersionRoundTrips(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	want := StateSnapshot{Cfg: schema.Config{Project: schema.Project{Name: "p"}}, ProxyVersion: "abc123"}
	require.NoError(t, WriteStateSnapshot(identity.Prod, "p", want))
	got, err := ReadStateSnapshot(identity.Prod, "p")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "abc123", got.ProxyVersion)
}

func TestStateSnapshotRoutesRoundTrips(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	want := StateSnapshot{
		Cfg: schema.Config{Project: schema.Project{Name: "p"}},
		Routes: []Route{
			{Hostname: "app.p.test", BackendHost: "127.42.0.9", BackendPort: 5173, Mode: ModeVM, Project: "p"},
			{Hostname: "db.p.test", BackendPort: 5432, Mode: ModeVM, Direct: true, Project: "p"},
			{Hostname: "api.p.test", BackendPort: 8080, Mode: ModeLocal, Project: "p"},
		},
	}
	require.NoError(t, WriteStateSnapshot(identity.Prod, "p", want))
	got, err := ReadStateSnapshot(identity.Prod, "p")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, want.Routes, got.Routes)
}

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}
