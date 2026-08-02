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
	// Volumes dir for project "live" exists AND a live VM matches.
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "volumes", "live", "data"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "volumes", "live", "data", "file"), []byte("x"), 0644))
	// State file for "live" exists too.
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "state"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "state", "live.json"), []byte("{}"), 0644))

	var buf bytes.Buffer
	err := runPurge(tmp, fakeVMLister{vms: []string{"live"}}, false, true, &buf)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "skipped 'live'")
	assert.NotContains(t, buf.String(), "would delete")
	// Volume dir still there.
	_, statErr := os.Stat(filepath.Join(tmp, "volumes", "live", "data"))
	assert.NoError(t, statErr)
}

func TestRunPurge_DryRun_ListsOrphans_DeletesNothing(t *testing.T) {
	tmp := t.TempDir()
	// Volumes dir for "orphan": no matching VM, no state file.
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "volumes", "orphan", "data"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "volumes", "orphan", "data", "file"), []byte("x"), 0644))

	var buf bytes.Buffer
	err := runPurge(tmp, fakeVMLister{vms: nil}, true, true, &buf) // dryRun=true, yes=true (yes ignored under dry-run)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "orphan")
	assert.Contains(t, buf.String(), "would delete") // dry-run wording
	// Still on disk.
	_, statErr := os.Stat(filepath.Join(tmp, "volumes", "orphan"))
	assert.NoError(t, statErr)
}

func TestRunPurge_YesFlag_DeletesOrphans(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "volumes", "orphan", "data"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "volumes", "orphan", "data", "file"), []byte("x"), 0644))

	var buf bytes.Buffer
	err := runPurge(tmp, fakeVMLister{vms: nil}, false, true, &buf) // dryRun=false, yes=true
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "deleted 'orphan'")
	// Gone from disk.
	_, statErr := os.Stat(filepath.Join(tmp, "volumes", "orphan"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestRunPurge_MixedLiveAndOrphan_SkipsLive_DeletesOrphan(t *testing.T) {
	tmp := t.TempDir()
	// Live project.
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "volumes", "live", "data"), 0700))
	// Orphan project.
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "volumes", "orphan", "data"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "volumes", "orphan", "data", "file"), []byte("x"), 0644))

	var buf bytes.Buffer
	err := runPurge(tmp, fakeVMLister{vms: []string{"live"}}, false, true, &buf)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "skipped 'live'")
	assert.Contains(t, buf.String(), "deleted 'orphan'")
	// live still there; orphan gone.
	_, statLive := os.Stat(filepath.Join(tmp, "volumes", "live"))
	assert.NoError(t, statLive)
	_, statOrphan := os.Stat(filepath.Join(tmp, "volumes", "orphan"))
	assert.True(t, os.IsNotExist(statOrphan))
}

func TestRunPurge_NoVolumesDir_Reports(t *testing.T) {
	tmp := t.TempDir()
	// No volumes/ subdir at all.
	var buf bytes.Buffer
	err := runPurge(tmp, fakeVMLister{vms: nil}, false, true, &buf)
	require.NoError(t, err)
	// Nothing to delete, nothing to skip.
	assert.True(t, strings.Contains(buf.String(), "nothing to purge") || buf.Len() == 0)
}
