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
		{Name: "myproj", VMIP: "192.168.64.7"},
	}))
	got, err := os.ReadFile(Path())
	require.NoError(t, err)
	want := loadGolden(t, "one_entry.golden")
	assert.Equal(t, want, string(got))
}

func TestEmit_MultipleEntries_SortedByName(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", filepath.Join(t.TempDir(), "rd"))
	// Unsorted input; expect output sorted by Name ascending.
	require.NoError(t, Emit([]Entry{
		{Name: "charlie", VMIP: "192.168.64.9"},
		{Name: "alpha", VMIP: "192.168.64.7"},
		{Name: "bravo", VMIP: "192.168.64.8"},
	}))
	got, err := os.ReadFile(Path())
	require.NoError(t, err)
	want := loadGolden(t, "three_entries.golden")
	assert.Equal(t, want, string(got))
}

func TestEmit_AtomicWrite(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", filepath.Join(t.TempDir(), "rd"))
	require.NoError(t, Emit([]Entry{
		{Name: "p", VMIP: "192.168.64.7"},
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

func TestEmit_RejectsUnsafeName(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", filepath.Join(t.TempDir(), "rd"))
	for _, name := range []string{
		"",
		"bad name\nHost pwned",
		"foo\"bar",
		"foo\x00bar",
		"foo bar",
		"foo *",
		"foo\tbar",
		"evil,*",  // comma is a Host-pattern separator in ssh_config
		"foo#bar", // # starts a comment
		"foo?bar", // ? is a single-char wildcard
		"foo!bar", // ! is a negation prefix in Host patterns
		"foo=bar", // = is a directive-value separator
		"../evil", // path traversal (name is also a path component)
		"foo/bar", // slash escapes the project dir
	} {
		t.Run(name, func(t *testing.T) {
			err := Emit([]Entry{{Name: name, VMIP: "1.2.3.4"}})
			require.Error(t, err, "must reject name %q", name)
		})
	}
}

func TestEmit_RejectsUnsafeVMIP(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", filepath.Join(t.TempDir(), "rd"))
	for _, ip := range []string{
		"not-an-ip",
		"1.2.3.4\n    ProxyCommand /bin/sh -c pwned",
		"1.2.3.4 5.6.7.8",
		"",
	} {
		t.Run(ip, func(t *testing.T) {
			err := Emit([]Entry{{Name: "p", VMIP: ip}})
			require.Error(t, err, "must reject VMIP %q", ip)
		})
	}
}
