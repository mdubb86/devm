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

// setupRuntimeDir points HOME at a tempdir and creates the daemon's
// runtime dir inside it — Emit no-ops when the dir doesn't exist (see
// its docstring), which is the daemon-uninstalled case; every test
// below exercises the daemon-running case, so we create the dir.
func setupRuntimeDir(t *testing.T, cfg identity.Config) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, os.MkdirAll(cfg.RuntimeDir(), 0o700))
}

func TestEmit_EmptyEntries_WritesHeaderOnly(t *testing.T) {
	setupRuntimeDir(t, identity.Prod)
	require.NoError(t, Emit(identity.Prod, nil))
	got, err := os.ReadFile(Path(identity.Prod))
	require.NoError(t, err)
	want := loadGolden(t, "empty.golden")
	assert.Equal(t, want, string(got))
}

func TestEmit_SingleEntry_GoldenFile(t *testing.T) {
	setupRuntimeDir(t, identity.Prod)
	require.NoError(t, Emit(identity.Prod, []Entry{
		{Name: "myproj"},
	}))
	got, err := os.ReadFile(Path(identity.Prod))
	require.NoError(t, err)
	want := loadGolden(t, "one_entry.golden")
	assert.Equal(t, want, string(got))
}

// TestEmit_NoOpWhenRuntimeDirMissing pins the "don't resurrect
// uninstalled state" contract: if cfg.RuntimeDir() doesn't exist,
// Emit returns nil without creating anything. Prevents a stop/teardown
// invoked right after uninstall from silently re-materializing the
// runtime dir + ssh_config file.
func TestEmit_NoOpWhenRuntimeDirMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Deliberately NOT creating identity.Prod.RuntimeDir().
	require.NoError(t, Emit(identity.Prod, []Entry{{Name: "x"}}))
	_, err := os.Stat(identity.Prod.RuntimeDir())
	assert.True(t, os.IsNotExist(err), "Emit must not create the runtime dir when it doesn't exist")
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
	err := emit(&buf, identity.Prod, entries)
	require.NoError(t, err)
	got := buf.String()
	assert.Contains(t, got, "Host devm-myapp")
	assert.Contains(t, got, "HostName             myapp.test")
	assert.Contains(t, got, "Port                 22")
	assert.NotContains(t, got, "127.0.0.1")
}

// TestEmit_E2EUsesE2ETLD verifies that emitting under identity.E2E
// renders HostName with the e2e TLD ("<name>.e2e.test") instead of
// Prod's "<name>.test" — the e2e resolver only handles /etc/resolver/
// e2e.test, so a stray ".test" hostname would never resolve under e2e.
func TestEmit_E2EUsesE2ETLD(t *testing.T) {
	entries := []Entry{
		{Name: "myapp", KeyPath: "/tmp/key", KnownHostsPath: "/tmp/known"},
	}
	var buf bytes.Buffer
	err := emit(&buf, identity.E2E, entries)
	require.NoError(t, err)
	got := buf.String()
	assert.Contains(t, got, "HostName             myapp.e2e.test")
	assert.NotContains(t, got, "myapp.test\n")
}

// TestEmit_HeaderIncludeLineMatchesCfg verifies the header's Include
// example points at the emitting cfg's own Path(cfg) — under E2E that's
// .../devm-e2e/ssh_config, a different runtime dir than Prod's
// .../devm/ssh_config, so an e2e-emitted file must not tell users to
// include prod's path.
func TestEmit_HeaderIncludeLineMatchesCfg(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, emit(&buf, identity.E2E, nil))
	got := buf.String()
	assert.Contains(t, got, `Include "`+Path(identity.E2E)+`"`)
	assert.NotContains(t, got, Path(identity.Prod))
}

func TestEmit_MultipleEntries_SortedByName(t *testing.T) {
	setupRuntimeDir(t, identity.Prod)
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
	setupRuntimeDir(t, identity.Prod)
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

func TestEnsureInclude_CreatesFileAndDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	require.NoError(t, EnsureInclude(identity.Prod))

	sshDir := filepath.Join(home, ".ssh")
	info, err := os.Stat(sshDir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	cfgPath := filepath.Join(sshDir, "config")
	got, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, `Include "`+Path(identity.Prod)+"\"\n", string(got))
}

func TestEnsureInclude_AppendsToExistingFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sshDir := filepath.Join(home, ".ssh")
	require.NoError(t, os.MkdirAll(sshDir, 0o700))
	cfgPath := filepath.Join(sshDir, "config")
	existing := "Host example\n    HostName example.com\n"
	require.NoError(t, os.WriteFile(cfgPath, []byte(existing), 0o600))

	require.NoError(t, EnsureInclude(identity.Prod))

	got, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	want := existing + `Include "` + Path(identity.Prod) + "\"\n"
	assert.Equal(t, want, string(got))
}

func TestEnsureInclude_HandlesMissingTrailingNewline(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sshDir := filepath.Join(home, ".ssh")
	require.NoError(t, os.MkdirAll(sshDir, 0o700))
	cfgPath := filepath.Join(sshDir, "config")
	existing := "Host example\n    HostName example.com" // no trailing \n
	require.NoError(t, os.WriteFile(cfgPath, []byte(existing), 0o600))

	require.NoError(t, EnsureInclude(identity.Prod))

	got, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	want := existing + "\n" + `Include "` + Path(identity.Prod) + "\"\n"
	assert.Equal(t, want, string(got))
}

func TestEnsureInclude_IdempotentSecondCall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	require.NoError(t, EnsureInclude(identity.Prod))
	require.NoError(t, EnsureInclude(identity.Prod))

	cfgPath := filepath.Join(home, ".ssh", "config")
	got, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	want := `Include "` + Path(identity.Prod) + `"`
	assert.Equal(t, 1, strings.Count(string(got), want))
}

func TestEnsureInclude_ProdAndE2ECoexist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	require.NoError(t, EnsureInclude(identity.Prod))
	require.NoError(t, EnsureInclude(identity.E2E))

	cfgPath := filepath.Join(home, ".ssh", "config")
	got, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.Contains(t, string(got), `Include "`+Path(identity.Prod)+`"`)
	assert.Contains(t, string(got), `Include "`+Path(identity.E2E)+`"`)
}

func TestRemoveInclude_RemovesOnlyMatchingLine(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sshDir := filepath.Join(home, ".ssh")
	require.NoError(t, os.MkdirAll(sshDir, 0o700))
	cfgPath := filepath.Join(sshDir, "config")
	userLine := `Include "~/some/other/config"`
	existing := userLine + "\n" +
		`Include "` + Path(identity.Prod) + "\"\n" +
		`Include "` + Path(identity.E2E) + "\"\n"
	require.NoError(t, os.WriteFile(cfgPath, []byte(existing), 0o600))

	require.NoError(t, RemoveInclude(identity.E2E))

	got, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	gotStr := string(got)
	assert.Contains(t, gotStr, userLine)
	assert.Contains(t, gotStr, `Include "`+Path(identity.Prod)+`"`)
	assert.NotContains(t, gotStr, `Include "`+Path(identity.E2E)+`"`)
}

func TestRemoveInclude_NoOpWhenAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sshDir := filepath.Join(home, ".ssh")
	require.NoError(t, os.MkdirAll(sshDir, 0o700))
	cfgPath := filepath.Join(sshDir, "config")
	existing := "Host example\n    HostName example.com\n"
	require.NoError(t, os.WriteFile(cfgPath, []byte(existing), 0o600))

	require.NoError(t, RemoveInclude(identity.Prod))

	got, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, existing, string(got))
}

func TestRemoveInclude_NoOpWhenFileMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	err := RemoveInclude(identity.Prod)
	require.NoError(t, err)

	_, statErr := os.Stat(filepath.Join(home, ".ssh", "config"))
	assert.True(t, os.IsNotExist(statErr))
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
