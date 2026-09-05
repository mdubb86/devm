package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolvePopTarget_ProjectRootRelative — a relative path resolves
// through the label→mirror table to the primary repo's mirror dir,
// with no `.vm`-suffixed indirection.
func TestResolvePopTarget_ProjectRootRelative(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate cfg.RuntimeDir() from the real HOME
	repoRoot := t.TempDir()
	url := "https://example.com/repo.git"
	primary := true
	pcfg := schema.Config{
		Project: schema.Project{Name: "t"},
		Repos: map[string]schema.RepoConfig{
			"main": {URL: &url, Primary: &primary},
		},
	}

	mirrorDir := filepath.Join(cfg.RuntimeDir(), "t", "repo")
	require.NoError(t, os.MkdirAll(mirrorDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mirrorDir, "vm-file.png"), []byte("x"), 0o644))

	got, err := resolvePopTarget("vm-file.png", repoRoot, pcfg)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(mirrorDir, "vm-file.png"), got)
	assert.NotContains(t, got, ".vm/")
}

// TestResolvePopTarget_NestedRelativePath — a relative path with
// subdirectories resolves the same way.
func TestResolvePopTarget_NestedRelativePath(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate cfg.RuntimeDir() from the real HOME
	repoRoot := t.TempDir()
	url := "https://example.com/repo.git"
	primary := true
	pcfg := schema.Config{
		Project: schema.Project{Name: "t3"},
		Repos: map[string]schema.RepoConfig{
			"main": {URL: &url, Primary: &primary},
		},
	}

	mirrorDir := filepath.Join(cfg.RuntimeDir(), "t3", "repo")
	require.NoError(t, os.MkdirAll(filepath.Join(mirrorDir, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mirrorDir, "sub", "near.png"), []byte("x"), 0o644))

	got, err := resolvePopTarget("sub/near.png", repoRoot, pcfg)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(mirrorDir, "sub", "near.png"), got)
}

// TestResolvePopTarget_AbsoluteGuestPath — an absolute guest-side path
// (e.g. pasted from guest output) resolves directly, regardless of
// repoRoot.
func TestResolvePopTarget_AbsoluteGuestPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate cfg.RuntimeDir() from the real HOME
	repoRoot := t.TempDir()
	url := "https://example.com/repo.git"
	primary := true
	pcfg := schema.Config{
		Project: schema.Project{Name: "t2"},
		Repos: map[string]schema.RepoConfig{
			"main": {URL: &url, Primary: &primary},
		},
	}

	mirrorDir := filepath.Join(cfg.RuntimeDir(), "t2", "repo")
	require.NoError(t, os.MkdirAll(filepath.Join(mirrorDir, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mirrorDir, "src", "abs.png"), []byte("x"), 0o644))

	got, err := resolvePopTarget("/home/devm/repo/src/abs.png", repoRoot, pcfg)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(mirrorDir, "src", "abs.png"), got)
}

// TestResolvePopTarget_NotFound — resolving to a mirror path whose
// file doesn't exist is an error, not a silent open of a missing file.
func TestResolvePopTarget_NotFound(t *testing.T) {
	repoRoot := t.TempDir()
	url := "https://example.com/repo.git"
	primary := true
	pcfg := schema.Config{
		Project: schema.Project{Name: "t4"},
		Repos: map[string]schema.RepoConfig{
			"main": {URL: &url, Primary: &primary},
		},
	}

	_, err := resolvePopTarget("nope.png", repoRoot, pcfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such file")
}

// TestResolvePopTarget_NoMirroredEntries — a project with no repos at
// all has no primary tree to resolve a relative path against.
func TestResolvePopTarget_NoMirroredEntries(t *testing.T) {
	repoRoot := t.TempDir()
	pcfg := schema.Config{Project: schema.Project{Name: "t5"}}

	_, err := resolvePopTarget("anything.png", repoRoot, pcfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no primary repo")
}

// TestRunPop_URL_PassedStraightToOpen — a URL arg bypasses cwd /
// config load / mirror table and lands in `open` verbatim.
func TestRunPop_URL_PassedStraightToOpen(t *testing.T) {
	var captured []string
	orig := popExecOpen
	popExecOpen = func(args ...string) error { captured = args; return nil }
	t.Cleanup(func() { popExecOpen = orig })

	err := runPop(popMacCmd, []string{"https://example.com/thing"})
	require.NoError(t, err)
	assert.Equal(t, []string{"https://example.com/thing"}, captured)
}

// TestRunPop_URL_ForwardsOpenArgs — `-a Firefox` etc. after `--` reach
// the `open` invocation alongside the URL.
func TestRunPop_URL_ForwardsOpenArgs(t *testing.T) {
	var captured []string
	orig := popExecOpen
	popExecOpen = func(args ...string) error { captured = args; return nil }
	t.Cleanup(func() { popExecOpen = orig })

	err := runPop(popMacCmd, []string{"http://localhost:3000", "--", "-a", "Firefox"})
	require.NoError(t, err)
	assert.Equal(t, []string{"http://localhost:3000", "-a", "Firefox"}, captured)
}

// TestResolvePopTarget_AbsolutePathOutsideAnyEntry — an absolute path
// that doesn't fall under any mirrored repo/volume is an error.
func TestResolvePopTarget_AbsolutePathOutsideAnyEntry(t *testing.T) {
	repoRoot := t.TempDir()
	url := "https://example.com/repo.git"
	primary := true
	pcfg := schema.Config{
		Project: schema.Project{Name: "t6"},
		Repos: map[string]schema.RepoConfig{
			"main": {URL: &url, Primary: &primary},
		},
	}

	_, err := resolvePopTarget("/etc/passwd", repoRoot, pcfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not inside any mirrored")
}

// writePopWorkspace writes a minimal valid devm.yaml (project name +
// a url-nil primary repo, so config.Load succeeds without touching a
// real git remote) into a fresh temp dir and returns its path.
func writePopWorkspace(t *testing.T, projectName string) string {
	t.Helper()
	workspace := t.TempDir()
	yaml := "project:\n  name: " + projectName + "\nrepos:\n  primary: {}\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "devm.yaml"), []byte(yaml), 0o644))
	return workspace
}

// TestRunPop_FallbackToCreateSession_FileArg — an absolute, out-of-mirror
// guest path with no trailing slash falls through resolvePopTarget's
// error into createPopSessionFn with is_dir=false, and opens whatever
// Mac path the daemon hands back.
func TestRunPop_FallbackToCreateSession_FileArg(t *testing.T) {
	workspace := writePopWorkspace(t, "myproj")

	origOpen := popExecOpen
	origCreate := createPopSessionFn
	t.Cleanup(func() { popExecOpen = origOpen; createPopSessionFn = origCreate })

	var openArgs []string
	popExecOpen = func(args ...string) error { openArgs = args; return nil }

	var gotProject, gotPath string
	var gotIsDir bool
	createPopSessionFn = func(ctx context.Context, ident identity.Config, projectName, guestPath string, isDir bool) (string, error) {
		gotProject = projectName
		gotPath = guestPath
		gotIsDir = isDir
		return "/scratch/xyz/index.html", nil
	}

	cmd := popMacCmd
	cmd.SetContext(context.Background())
	t.Chdir(workspace)

	err := runPop(cmd, []string{"/tmp/site/index.html"})
	require.NoError(t, err)
	assert.Equal(t, "myproj", gotProject)
	assert.Equal(t, "/tmp/site/index.html", gotPath)
	assert.False(t, gotIsDir)
	assert.Equal(t, []string{"/scratch/xyz/index.html"}, openArgs)
}

// TestRunPop_FallbackToCreateSession_DirArgWithTrailingSlash — a
// trailing slash on the out-of-mirror arg signals is_dir=true, and the
// slash-terminated arg is forwarded to the daemon unchanged.
func TestRunPop_FallbackToCreateSession_DirArgWithTrailingSlash(t *testing.T) {
	workspace := writePopWorkspace(t, "myproj2")

	origOpen := popExecOpen
	origCreate := createPopSessionFn
	t.Cleanup(func() { popExecOpen = origOpen; createPopSessionFn = origCreate })

	var openArgs []string
	popExecOpen = func(args ...string) error { openArgs = args; return nil }

	var gotProject, gotPath string
	var gotIsDir bool
	createPopSessionFn = func(ctx context.Context, ident identity.Config, projectName, guestPath string, isDir bool) (string, error) {
		gotProject = projectName
		gotPath = guestPath
		gotIsDir = isDir
		return "/scratch/abc", nil
	}

	cmd := popMacCmd
	cmd.SetContext(context.Background())
	t.Chdir(workspace)

	err := runPop(cmd, []string{"/tmp/site/"})
	require.NoError(t, err)
	assert.Equal(t, "myproj2", gotProject)
	assert.Equal(t, "/tmp/site/", gotPath)
	assert.True(t, gotIsDir)
	assert.Equal(t, []string{"/scratch/abc"}, openArgs)
}

// TestRunPop_FallbackRefusesRelativeArgOutOfMirror — a relative arg
// that also isn't in any mirror can't be forwarded to the daemon (it
// has no cwd on the guest to resolve against), so runPop refuses
// before ever calling createPopSessionFn.
func TestRunPop_FallbackRefusesRelativeArgOutOfMirror(t *testing.T) {
	workspace := writePopWorkspace(t, "myproj3")

	origOpen := popExecOpen
	origCreate := createPopSessionFn
	t.Cleanup(func() { popExecOpen = origOpen; createPopSessionFn = origCreate })

	popExecOpen = func(args ...string) error {
		t.Fatal("popExecOpen should not be called")
		return nil
	}
	createCalled := false
	createPopSessionFn = func(ctx context.Context, ident identity.Config, projectName, guestPath string, isDir bool) (string, error) {
		createCalled = true
		return "", nil
	}

	cmd := popMacCmd
	cmd.SetContext(context.Background())
	t.Chdir(workspace)

	err := runPop(cmd, []string{"somefile.html"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not an absolute guest path")
	assert.False(t, createCalled)
}
