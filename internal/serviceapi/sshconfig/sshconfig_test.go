package sshconfig

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helper
func loadGolden(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return strings.ReplaceAll(string(raw), "{{RUNTIME_DIR}}", os.Getenv("DEVM_RUNTIME_DIR"))
}

func TestEmit_EmptyEntries_WritesHeaderOnly(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", filepath.Join(t.TempDir(), "rd"))
	require.NoError(t, Emit(nil))
	got, err := os.ReadFile(Path())
	require.NoError(t, err)
	want := loadGolden(t, "empty.golden")
	assert.Equal(t, want, string(got))
}

func TestEmit_SingleEntry_GoldenFile(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", filepath.Join(t.TempDir(), "rd"))
	require.NoError(t, Emit([]Entry{
		{ProjectID: "myproj", VMName: "myproj-vm", VMIP: "192.168.64.7"},
	}))
	got, err := os.ReadFile(Path())
	require.NoError(t, err)
	want := loadGolden(t, "one_entry.golden")
	assert.Equal(t, want, string(got))
}

func TestEmit_MultipleEntries_SortedByVMName(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", filepath.Join(t.TempDir(), "rd"))
	// Unsorted input; expect output sorted by VMName ascending.
	require.NoError(t, Emit([]Entry{
		{ProjectID: "c", VMName: "charlie-vm", VMIP: "192.168.64.9"},
		{ProjectID: "a", VMName: "alpha-vm", VMIP: "192.168.64.7"},
		{ProjectID: "b", VMName: "bravo-vm", VMIP: "192.168.64.8"},
	}))
	got, err := os.ReadFile(Path())
	require.NoError(t, err)
	want := loadGolden(t, "three_entries.golden")
	assert.Equal(t, want, string(got))
}

func TestEmit_AtomicWrite(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", filepath.Join(t.TempDir(), "rd"))
	require.NoError(t, Emit([]Entry{
		{ProjectID: "p", VMName: "p-vm", VMIP: "192.168.64.7"},
	}))
	// Only ssh_config remains in RuntimeDir root; no temp files linger.
	entries, err := os.ReadDir(filepath.Dir(Path()))
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	assert.Equal(t, []string{"ssh_config"}, names)
}

func TestEmit_RejectsUnsafeVMName(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", filepath.Join(t.TempDir(), "rd"))
	err := Emit([]Entry{{ProjectID: "p", VMName: "bad name\nHost pwned", VMIP: "1.2.3.4"}})
	require.Error(t, err)
}
