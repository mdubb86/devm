package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeVMLister is the test double for the tart client so tests don't
// depend on a live Tart install.
type fakeVMLister struct{ vms []string }

func (f fakeVMLister) List() ([]string, error) { return f.vms, nil }

func TestRunPurge_NoOrphans_NoCandidates(t *testing.T) {
	tmp := t.TempDir()
	// Mirror dir for project "live" exists AND a live VM matches.
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "live", "data"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "live", "data", "file"), []byte("x"), 0644))
	// State file for "live" exists too.
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "state"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "state", "live.json"), []byte("{}"), 0644))

	var buf bytes.Buffer
	err := runPurge(tmp, fakeVMLister{vms: []string{"live"}}, false, true, &buf)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "skipped 'live'")
	assert.NotContains(t, buf.String(), "would delete")
	// Mirror dir still there.
	_, statErr := os.Stat(filepath.Join(tmp, "live", "data"))
	assert.NoError(t, statErr)
}

func TestRunPurge_DryRun_ListsOrphans_DeletesNothing(t *testing.T) {
	tmp := t.TempDir()
	// Mirror dir for "orphan": no matching VM, no state file.
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "orphan", "data"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "orphan", "data", "file"), []byte("x"), 0644))

	var buf bytes.Buffer
	err := runPurge(tmp, fakeVMLister{vms: nil}, true, true, &buf) // dryRun=true, yes=true (yes ignored under dry-run)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "orphan")
	assert.Contains(t, buf.String(), "would delete") // dry-run wording
	// Still on disk.
	_, statErr := os.Stat(filepath.Join(tmp, "orphan"))
	assert.NoError(t, statErr)
}

func TestRunPurge_YesFlag_DeletesOrphans(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "orphan", "data"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "orphan", "data", "file"), []byte("x"), 0644))

	var buf bytes.Buffer
	err := runPurge(tmp, fakeVMLister{vms: nil}, false, true, &buf) // dryRun=false, yes=true
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "deleted 'orphan'")
	// Gone from disk.
	_, statErr := os.Stat(filepath.Join(tmp, "orphan"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestRunPurge_MixedLiveAndOrphan_SkipsLive_DeletesOrphan(t *testing.T) {
	tmp := t.TempDir()
	// Live project.
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "live", "data"), 0700))
	// Orphan project.
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "orphan", "data"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "orphan", "data", "file"), []byte("x"), 0644))

	var buf bytes.Buffer
	err := runPurge(tmp, fakeVMLister{vms: []string{"live"}}, false, true, &buf)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "skipped 'live'")
	assert.Contains(t, buf.String(), "deleted 'orphan'")
	// live still there; orphan gone.
	_, statLive := os.Stat(filepath.Join(tmp, "live"))
	assert.NoError(t, statLive)
	_, statOrphan := os.Stat(filepath.Join(tmp, "orphan"))
	assert.True(t, os.IsNotExist(statOrphan))
}

func TestRunPurge_NoRuntimeDir_Reports(t *testing.T) {
	tmp := t.TempDir()
	// Point at a subdir that doesn't exist at all.
	missing := filepath.Join(tmp, "does-not-exist")
	var buf bytes.Buffer
	err := runPurge(missing, fakeVMLister{vms: nil}, false, true, &buf)
	require.NoError(t, err)
	// Nothing to delete, nothing to skip.
	assert.True(t, strings.Contains(buf.String(), "nothing to purge") || buf.Len() == 0)
}

// TestRunPurge_SkipsDevmInternalDirs pins the skip-list: devm-internal
// storage dirs living directly under runtimeDir (bin, state,
// iron-proxy, mutagen, ssh, secrets, ca, softnet-bin, volumes, and any
// dotfile) must never be walked as if they were project mirror dirs —
// even when they have no matching VM or state file, which is always
// true since they aren't project IDs at all.
func TestRunPurge_SkipsDevmInternalDirs(t *testing.T) {
	tmp := t.TempDir()
	internalDirs := []string{
		"bin", "state", "iron-proxy", "mutagen",
		"ssh", "secrets", "ca", "softnet-bin", "volumes",
		".hidden",
	}
	for _, d := range internalDirs {
		require.NoError(t, os.MkdirAll(filepath.Join(tmp, d, "stuff"), 0700))
	}
	// One real orphaned project alongside them.
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "orphan", "data"), 0700))

	var buf bytes.Buffer
	err := runPurge(tmp, fakeVMLister{vms: nil}, false, true, &buf)
	require.NoError(t, err)

	assert.Contains(t, buf.String(), "deleted 'orphan'")
	for _, d := range internalDirs {
		assert.NotContains(t, buf.String(), "'"+d+"'")
		_, statErr := os.Stat(filepath.Join(tmp, d, "stuff"))
		assert.NoError(t, statErr, "devm-internal dir %q must survive purge", d)
	}
}

// TestRunPurge_StateFilePresence_Skips confirms the state-file check
// alone is sufficient to protect a project dir, independent of the
// tart-VM check.
func TestRunPurge_StateFilePresence_Skips(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "proj", "data"), 0700))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "state"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "state", "proj.json"), []byte("{}"), 0644))

	var buf bytes.Buffer
	// No VM named "proj" — only the state file should protect it.
	err := runPurge(tmp, fakeVMLister{vms: nil}, false, true, &buf)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "skipped 'proj': state file exists")
	_, statErr := os.Stat(filepath.Join(tmp, "proj", "data"))
	assert.NoError(t, statErr)
}

// TestRunPurge_TartVMPresence_Skips confirms the tart-VM check alone
// is sufficient to protect a project dir, independent of the
// state-file check.
func TestRunPurge_TartVMPresence_Skips(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "proj", "data"), 0700))
	// No state dir/file at all — only the live VM should protect it.

	var buf bytes.Buffer
	err := runPurge(tmp, fakeVMLister{vms: []string{"proj"}}, false, true, &buf)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "skipped 'proj': VM still exists")
	_, statErr := os.Stat(filepath.Join(tmp, "proj", "data"))
	assert.NoError(t, statErr)
}
