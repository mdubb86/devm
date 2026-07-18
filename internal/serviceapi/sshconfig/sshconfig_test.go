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
		{Name: "myproj", Host: "127.0.0.1", Port: 2200},
	}))
	got, err := os.ReadFile(Path())
	require.NoError(t, err)
	want := loadGolden(t, "one_entry.golden")
	assert.Equal(t, want, string(got))
}

// TestEmit_SingleEntry_PointsAtHostLoopback verifies the block now emits
// HostName 127.0.0.1 + the per-project SSH host port — under softnet the
// guest IP isn't Mac-routable, so 127.0.0.1:<host port> is the only
// address that reaches the VM's :22.
func TestEmit_SingleEntry_PointsAtHostLoopback(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", filepath.Join(t.TempDir(), "rd"))
	require.NoError(t, Emit([]Entry{
		{Name: "myproj", Host: "127.0.0.1", Port: 2200},
	}))
	got, err := os.ReadFile(Path())
	require.NoError(t, err)
	assert.Contains(t, string(got), "HostName             127.0.0.1")
	assert.Contains(t, string(got), "Port                 2200")
}

func TestEmit_MultipleEntries_SortedByName(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", filepath.Join(t.TempDir(), "rd"))
	// Unsorted input; expect output sorted by Name ascending.
	require.NoError(t, Emit([]Entry{
		{Name: "charlie", Host: "127.0.0.1", Port: 2203},
		{Name: "alpha", Host: "127.0.0.1", Port: 2201},
		{Name: "bravo", Host: "127.0.0.1", Port: 2202},
	}))
	got, err := os.ReadFile(Path())
	require.NoError(t, err)
	want := loadGolden(t, "three_entries.golden")
	assert.Equal(t, want, string(got))
}

func TestEmit_AtomicWrite(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", filepath.Join(t.TempDir(), "rd"))
	require.NoError(t, Emit([]Entry{
		{Name: "p", Host: "127.0.0.1", Port: 2200},
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
			err := Emit([]Entry{{Name: name, Host: "127.0.0.1", Port: 2200}})
			require.Error(t, err, "must reject name %q", name)
		})
	}
}

func TestEmit_RejectsUnsafeHost(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", filepath.Join(t.TempDir(), "rd"))
	for _, host := range []string{
		"not-an-ip",
		"1.2.3.4\n    ProxyCommand /bin/sh -c pwned",
		"1.2.3.4 5.6.7.8",
		"",
	} {
		t.Run(host, func(t *testing.T) {
			err := Emit([]Entry{{Name: "p", Host: host, Port: 2200}})
			require.Error(t, err, "must reject host %q", host)
		})
	}
}

func TestEmit_RejectsUnsafePort(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", filepath.Join(t.TempDir(), "rd"))
	for _, port := range []int{0, -1, 65536, 100000} {
		err := Emit([]Entry{{Name: "p", Host: "127.0.0.1", Port: port}})
		require.Error(t, err, "must reject port %d", port)
	}
}
