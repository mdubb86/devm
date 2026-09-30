package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// popTestOption configures setupTestProject.
type popTestOption func(*popTestConfig)

type popTestConfig struct {
	mirrorPaths []string
	scratchHome string
}

// withMirrorFile seeds the project's mirror table with an entry at
// guestPath (an absolute guest path under /home/devm/<project>/), so
// resolvePopTarget resolves it through the mirror table. A trailing
// "/" creates a directory instead of a file.
func withMirrorFile(guestPath string) popTestOption {
	return func(c *popTestConfig) { c.mirrorPaths = append(c.mirrorPaths, guestPath) }
}

// withScratchRoot points cfg.RuntimeDir() — and therefore
// serviceapi.PopScratchRoot — under dir, by setting HOME for the
// duration of the test. dir is a valid prefix of the resulting
// scratch path (RuntimeDir nests "Library/Application Support/<name>"
// under HOME).
func withScratchRoot(dir string) popTestOption {
	return func(c *popTestConfig) { c.scratchHome = dir }
}

// setupTestProject wires discoverProjectFn to a fresh temp project
// named projectName, with an explicit URL + explicit label on its
// primary repo so BuildEntities never shells out to git or derives a
// label from a real checkout. tld documents the TLD the test expects
// (a `go test`-built binary always runs under identity.Prod, so it's
// always "test" — identity.Profile is a build-time ldflag, not
// something a unit test can override).
func setupTestProject(t *testing.T, projectName, tld string, opts ...popTestOption) {
	t.Helper()
	_ = tld

	c := &popTestConfig{}
	for _, opt := range opts {
		opt(c)
	}

	home := c.scratchHome
	if home == "" {
		home = t.TempDir()
	}
	t.Setenv("HOME", home)

	macCwd := t.TempDir()
	yaml := "project:\n" +
		"  name: " + projectName + "\n" +
		"repos:\n" +
		"  main:\n" +
		"    url: https://example.com/repo.git\n" +
		"    primary: true\n" +
		"    label: " + projectName + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(macCwd, "devm.yaml"), []byte(yaml), 0o644))

	discoverProjectFn = func() (LocalProject, error) {
		return LocalProject{Name: projectName, MacCwd: macCwd}, nil
	}
	t.Cleanup(func() { discoverProjectFn = discoverProject })

	guestRoot := "/home/devm/" + projectName + "/"
	for _, guestPath := range c.mirrorPaths {
		rel := strings.TrimPrefix(guestPath, guestRoot)
		// mountPassthrough resolves a mirrored entry to
		// <RuntimeDir>/<project>/<label>/<rel-to-entry-guest-path>;
		// the primary repo's label is projectName here (set
		// explicitly above), so its mirror dir is
		// <RuntimeDir>/<project>/<project>/.
		macPath := filepath.Join(cfg.RuntimeDir(), projectName, projectName, rel)
		if strings.HasSuffix(guestPath, "/") {
			require.NoError(t, os.MkdirAll(macPath, 0o755))
		} else {
			require.NoError(t, os.MkdirAll(filepath.Dir(macPath), 0o755))
			require.NoError(t, os.WriteFile(macPath, []byte("x"), 0o644))
		}
	}
}

// stubResolveProjectFn overrides discoverProjectFn for the duration of
// the test to bypass the filesystem walk, returning name/macCwd as the
// discovered project. Shared by other cmd/devm test files
// (status_test.go, volume_test.go), not just pop's own tests.
func stubResolveProjectFn(t *testing.T, name, macCwd string) {
	t.Helper()
	orig := discoverProjectFn
	discoverProjectFn = func() (LocalProject, error) {
		return LocalProject{Name: name, MacCwd: macCwd}, nil
	}
	t.Cleanup(func() { discoverProjectFn = orig })
}

// TestRunPop_DefaultBuildsFilestashURL asserts the default flow
// (no --native flag) opens https://files.<project>.<tld>/files/local<abs-path>.
func TestRunPop_DefaultBuildsFilestashURL(t *testing.T) {
	var got string
	popExecOpen = func(args ...string) error { got = args[0]; return nil }
	t.Cleanup(func() { popExecOpen = defaultPopExecOpen })

	// A path resolvable through the mirror table.
	setupTestProject(t, "myproj", "test", withMirrorFile("/home/devm/myproj/foo.txt"))
	err := runPop(popCmd, []string{"foo.txt"})
	require.NoError(t, err)

	assert.Equal(t,
		"https://files.myproj.test/files/local/home/devm/myproj/foo.txt",
		got,
	)
}

// TestRunPop_NativeInMirror asserts --native + in-mirror path calls
// open with the Mac-side mirror path, not a URL.
func TestRunPop_NativeInMirror(t *testing.T) {
	var got []string
	popExecOpen = func(args ...string) error { got = args; return nil }
	t.Cleanup(func() { popExecOpen = defaultPopExecOpen })

	setupTestProject(t, "myproj", "test", withMirrorFile("/home/devm/myproj/foo.txt"))
	popNativeFlag = true
	t.Cleanup(func() { popNativeFlag = false })

	err := runPop(popCmd, []string{"foo.txt"})
	require.NoError(t, err)
	require.NotEmpty(t, got)
	assert.NotContains(t, got[0], "https://", "must open mac path, not URL")
	assert.Contains(t, got[0], "myproj/foo.txt")
}

// TestRunPop_NativeOutOfMirror_CopiesToScratch asserts --native + out-of-mirror
// runs tart exec cat and opens the scratch file.
func TestRunPop_NativeOutOfMirror_CopiesToScratch(t *testing.T) {
	scratchDir := t.TempDir()
	var catCalled bool
	var openArg string
	tartExecCatFn = func(vm, guestPath, macDest string) error {
		catCalled = true
		assert.Equal(t, "/etc/motd", guestPath)
		return os.WriteFile(macDest, []byte("motd bytes\n"), 0o644)
	}
	popExecOpen = func(args ...string) error { openArg = args[0]; return nil }
	t.Cleanup(func() {
		tartExecCatFn = defaultTartExecCat
		popExecOpen = defaultPopExecOpen
	})

	setupTestProject(t, "myproj", "test", withScratchRoot(scratchDir))
	popNativeFlag = true
	t.Cleanup(func() { popNativeFlag = false })

	err := runPop(popCmd, []string{"/etc/motd"})
	require.NoError(t, err)
	assert.True(t, catCalled)
	assert.True(t, strings.HasPrefix(openArg, scratchDir), "open with scratch file, got %q", openArg)
	body, _ := os.ReadFile(openArg)
	assert.Equal(t, "motd bytes\n", string(body))
}

// TestRunPop_NativeOutOfMirror_SymlinkResolves — Review Focus #4:
// a symlink target has its bytes cp'd, not the symlink metadata.
func TestRunPop_NativeOutOfMirror_SymlinkResolves(t *testing.T) {
	// tart exec cat naturally follows symlinks (cat reads the file it
	// resolves to); this test pins that we don't accidentally use
	// something like tar or cp -P that preserves the symlink.
	var caughtPath string
	tartExecCatFn = func(vm, guestPath, macDest string) error {
		caughtPath = guestPath
		return os.WriteFile(macDest, []byte("target\n"), 0o644)
	}
	popExecOpen = func(args ...string) error { return nil }
	t.Cleanup(func() {
		tartExecCatFn = defaultTartExecCat
		popExecOpen = defaultPopExecOpen
	})
	setupTestProject(t, "myproj", "test", withScratchRoot(t.TempDir()))
	popNativeFlag = true
	t.Cleanup(func() { popNativeFlag = false })

	err := runPop(popCmd, []string{"/tmp/some-symlink"})
	require.NoError(t, err)
	assert.Equal(t, "/tmp/some-symlink", caughtPath,
		"guest path is passed to tart exec cat verbatim; cat resolves the symlink")
}

// TestRunPop_NativeOnDirectory_Errors — Review Focus #5:
// --native + a directory returns a clear error, not a cryptic cat failure.
func TestRunPop_NativeOnDirectory_Errors(t *testing.T) {
	tartExecCatFn = func(vm, guestPath, macDest string) error {
		// Real cat on a directory: 'cat: /some/dir: Is a directory' → non-zero exit.
		return errors.New("cat: /some/dir: Is a directory")
	}
	t.Cleanup(func() { tartExecCatFn = defaultTartExecCat })
	setupTestProject(t, "myproj", "test", withScratchRoot(t.TempDir()))
	popNativeFlag = true
	t.Cleanup(func() { popNativeFlag = false })

	err := runPop(popCmd, []string{"/some/dir"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "directories not supported under --native",
		"error must name the constraint AND the alternative")
	assert.Contains(t, err.Error(), "default flow",
		"error must mention default flow works for directories")
}

// TestRunPop_DefaultOnDirectory_BuildsListingURL — Review Focus #5 opposite:
// default flow works for directories (filestash renders the listing).
func TestRunPop_DefaultOnDirectory_BuildsListingURL(t *testing.T) {
	var got string
	popExecOpen = func(args ...string) error { got = args[0]; return nil }
	t.Cleanup(func() { popExecOpen = defaultPopExecOpen })
	setupTestProject(t, "myproj", "test", withMirrorFile("/home/devm/myproj/subdir/"))

	err := runPop(popCmd, []string{"subdir/"})
	require.NoError(t, err)
	assert.Equal(t,
		"https://files.myproj.test/files/local/home/devm/myproj/subdir/",
		got,
	)
}
