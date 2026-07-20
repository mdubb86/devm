package sshconfig

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mdubb86/devm/internal/identity"
)

// helper
func loadGolden(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return strings.ReplaceAll(string(raw), "{{RUNTIME_DIR}}", identity.Prod.RuntimeDir())
}

func TestEmit_EmptyEntries_WritesHeaderOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, Emit(identity.Prod, nil))
	got, err := os.ReadFile(Path(identity.Prod))
	require.NoError(t, err)
	want := loadGolden(t, "empty.golden")
	assert.Equal(t, want, string(got))
}

func TestEmit_SingleEntry_GoldenFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, Emit(identity.Prod, []Entry{
		{Name: "myproj"},
	}))
	got, err := os.ReadFile(Path(identity.Prod))
	require.NoError(t, err)
	want := loadGolden(t, "one_entry.golden")
	assert.Equal(t, want, string(got))
}

// TestEmit_UsesDNSHostname verifies the block points HostName at the
// project's DNS name (<name>.test) on the fixed port 22 — softnet binds
// every project's guest :22 on its allocated ProjectIP and DNS answers
// <name>.test -> ProjectIP, so there is no more daemon-allocated host
// port or loopback address to resolve.
func TestEmit_UsesDNSHostname(t *testing.T) {
	entries := []Entry{
		{Name: "myapp", KeyPath: "/tmp/key", KnownHostsPath: "/tmp/known"},
	}
	var buf bytes.Buffer
	err := emit(&buf, entries)
	require.NoError(t, err)
	got := buf.String()
	assert.Contains(t, got, "Host devm-myapp")
	assert.Contains(t, got, "HostName             myapp.test")
	assert.Contains(t, got, "Port                 22")
	assert.NotContains(t, got, "127.0.0.1")
}

func TestEmit_MultipleEntries_SortedByName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Unsorted input; expect output sorted by Name ascending.
	require.NoError(t, Emit(identity.Prod, []Entry{
		{Name: "charlie"},
		{Name: "alpha"},
		{Name: "bravo"},
	}))
	got, err := os.ReadFile(Path(identity.Prod))
	require.NoError(t, err)
	want := loadGolden(t, "three_entries.golden")
	assert.Equal(t, want, string(got))
}

func TestEmit_AtomicWrite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, Emit(identity.Prod, []Entry{
		{Name: "p"},
	}))
	// Only ssh_config remains in RuntimeDir root; no temp files linger.
	entries, err := os.ReadDir(filepath.Dir(Path(identity.Prod)))
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
	t.Setenv("HOME", t.TempDir())
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
			err := Emit(identity.Prod, []Entry{{Name: name}})
			require.Error(t, err, "must reject name %q", name)
		})
	}
}
