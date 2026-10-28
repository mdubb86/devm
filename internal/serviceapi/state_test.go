package serviceapi

import (
	"os"
	"path/filepath"
	"strings"
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

// TestReadStateSnapshot_Malformed_QuarantinesAndSelfHeals pins the
// Finding-5 fix: a snapshot that can't parse (typical cause: schema
// change in a newer daemon) is renamed to a `.corrupt-<nanos>`
// forensic file and reported as absent, so the daemon rebuilds state
// on the next reconcile instead of degrading silently for the rest
// of the day. Pre-fix, the parse error bubbled up, every caller
// logged-and-continued, and `devm status` reported an unexplained
// `proxy: UNHEALTHY` with no connection back to the boot-time log.
func TestReadStateSnapshot_Malformed_QuarantinesAndSelfHeals(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, os.MkdirAll(StateDir(identity.Prod), 0o700))
	junkPath := filepath.Join(StateDir(identity.Prod), "junk.json")
	require.NoError(t, os.WriteFile(junkPath, []byte("{oh no"), 0o600))

	got, err := ReadStateSnapshot(identity.Prod, "junk")
	require.NoError(t, err, "parse failure must self-heal, not propagate")
	assert.Nil(t, got, "unparseable file is treated as absent — daemon will rebuild")

	_, statErr := os.Stat(junkPath)
	assert.True(t, os.IsNotExist(statErr), "original file must have been moved aside")

	entries, err := os.ReadDir(StateDir(identity.Prod))
	require.NoError(t, err)
	var quarantined string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "junk.json.corrupt-") {
			quarantined = e.Name()
			break
		}
	}
	require.NotEmpty(t, quarantined, "a .corrupt-<nanos> forensic copy must be left in StateDir")

	// Second call sees no file and returns (nil, nil) — self-heal
	// is a one-shot per corrupt file, not an infinite rename loop.
	got2, err2 := ReadStateSnapshot(identity.Prod, "junk")
	require.NoError(t, err2)
	assert.Nil(t, got2)
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

func TestStateSnapshotMacCwdRoundTrips(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	want := StateSnapshot{
		Cfg:    schema.Config{Project: schema.Project{Name: "p"}},
		MacCwd: "/Users/dev/projects/p",
	}
	require.NoError(t, WriteStateSnapshot(identity.Prod, "p", want))
	got, err := ReadStateSnapshot(identity.Prod, "p")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "/Users/dev/projects/p", got.MacCwd)
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
