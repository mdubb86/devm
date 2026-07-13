package reconcile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTartForApplyLive returns a *tart.Tart pointing at a shell script
// that records every invocation's argv (space-joined) as one line in
// the returned call-log file, then exits 0. Use countCalls to inspect
// which operations ran (bundle pipe via `exec -i`, dispatcher via
// install-templates.sh, etc.) without needing a mock interface —
// *tart.Tart's Path field is already the seam the rest of the test
// suite uses.
func fakeTartForApplyLive(t *testing.T, dir string) (*tart.Tart, string) {
	t.Helper()
	log := filepath.Join(dir, "tart-calls.txt")
	bin := filepath.Join(dir, "fake-tart")
	script := "#!/bin/sh\necho \"$*\" >> " + log + "\nexec true\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = bin
	return tr, log
}

// countCalls returns how many recorded invocations in logPath contain
// substr. Missing log file (nothing was ever called) counts as zero.
func countCalls(t *testing.T, logPath, substr string) int {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return 0
	}
	require.NoError(t, err)
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		if strings.Contains(line, substr) {
			n++
		}
	}
	return n
}

func TestApplyLive_SkipsRecreateKinds(t *testing.T) {
	dir := t.TempDir()
	tr, _ := fakeTartForApplyLive(t, dir)
	err := ApplyLive(tr, "x", []Change{
		{Kind: KindInstallChange},
		{Kind: KindMaskAddRemove},
	}, schema.Config{}, dir)
	assert.NoError(t, err)
}

func TestApplyLive_PortKindsAreNoOps(t *testing.T) {
	// Port kinds no longer trigger sbx publish; they are silently accepted
	// (Caddyfile reload happens via the provisioner pattern, not here).
	dir := t.TempDir()
	tr, _ := fakeTartForApplyLive(t, dir)
	err := ApplyLive(tr, "x", []Change{
		{Kind: KindPortAdd, Service: "api", Key: "8080", New: "8080"},
		{Kind: KindPortRemove, Service: "api", Key: "8080", Old: "8080"},
		{Kind: KindPortChange, Service: "api", Key: "9090", Old: "8080", New: "9090"},
	}, schema.Config{}, dir)
	assert.NoError(t, err)
}

// TestApplyLive_EnvChange_PipesBundle_NoWorkspaceWrite is a regression
// test for the bundle refactor: ApplyLive on an env change must pipe a
// bundle to the guest via ExecStdin — NOT write repoRoot/.devm/.env on
// the host. If we ever regress and write to the workspace, the user's
// project tree gets devm-internal state, which the whole bundle
// refactor exists to prevent.
func TestApplyLive_EnvChange_PipesBundle_NoWorkspaceWrite(t *testing.T) {
	dir := t.TempDir()
	tr, log := fakeTartForApplyLive(t, dir)
	cfg := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p-vm"},
		Env:     map[string]schema.EnvValue{"FOO": {Literal: "new"}},
	}

	err := ApplyLive(tr, "p-vm", []Change{
		{Kind: KindEnvChange, Key: "FOO", Old: "old", New: "new"},
	}, cfg, dir)
	require.NoError(t, err)

	// No host-side .devm/ writes.
	_, statErr := os.Stat(filepath.Join(dir, ".devm"))
	require.True(t, os.IsNotExist(statErr),
		"ApplyLive must NOT write .devm/ to the workspace; got: %v", statErr)

	// Bundle piped via ExecStdin (`tart exec -i ...`), exactly once, and
	// no dispatcher run (no template changes in this case).
	assert.Equal(t, 1, countCalls(t, log, "exec -i"), "expected one ExecStdin (bundle pipe) call")
	assert.Equal(t, 0, countCalls(t, log, "install-templates.sh"), "no template changes; dispatcher must not run")
}

func TestApplyLive_EnvAddAndRemove_AlsoPipeBundle_NoWorkspaceWrite(t *testing.T) {
	for _, kind := range []ChangeKind{KindEnvAdd, KindEnvRemove} {
		dir := t.TempDir()
		tr, log := fakeTartForApplyLive(t, dir)
		cfg := schema.Config{Env: map[string]schema.EnvValue{"K": {Literal: "v"}}}
		err := ApplyLive(tr, "x", []Change{
			{Kind: kind, Key: "K", New: "v"},
		}, cfg, dir)
		require.NoError(t, err, "kind=%v", kind)

		_, statErr := os.Stat(filepath.Join(dir, ".devm"))
		require.True(t, os.IsNotExist(statErr), ".devm/ must not be written for kind=%v; got: %v", kind, statErr)
		assert.Equal(t, 1, countCalls(t, log, "exec -i"), "expected one ExecStdin call for kind=%v", kind)
	}
}

func TestApplyLive_MultipleEnvChanges_SingleBundlePipe(t *testing.T) {
	dir := t.TempDir()
	tr, log := fakeTartForApplyLive(t, dir)
	cfg := schema.Config{Env: map[string]schema.EnvValue{"A": {Literal: "1"}, "B": {Literal: "2"}, "C": {Literal: "3"}}}
	err := ApplyLive(tr, "x", []Change{
		{Kind: KindEnvAdd, Key: "A", New: "1"},
		{Kind: KindEnvChange, Key: "B", Old: "x", New: "2"},
		{Kind: KindEnvAdd, Key: "C", New: "3"},
	}, cfg, dir)
	require.NoError(t, err)
	assert.Equal(t, 1, countCalls(t, log, "exec -i"), "multiple env changes must still coalesce into a single bundle pipe")
}

// TestApplyLive_PathChange_PipesBundle_NoWorkspaceWrite is a regression
// test for test_35 (e2e): a path-only change (no accompanying env
// change) must still trigger the bundle rebuild + pipe, because
// render.RenderEnv folds cfg.Path into the same .env's PATH= line —
// there's no separate path-only artifact. Before this fix, ApplyLive's
// switch had no case for KindPathChange, so a path-only reconcile
// silently did nothing: the guest's /opt/devm/.env was never rewritten
// and the next shell never saw the new PATH.
func TestApplyLive_PathChange_PipesBundle_NoWorkspaceWrite(t *testing.T) {
	dir := t.TempDir()
	tr, log := fakeTartForApplyLive(t, dir)
	cfg := schema.Config{Path: []string{"/workspace/bin"}}

	err := ApplyLive(tr, "p-vm", []Change{
		{Kind: KindPathChange, Old: "", New: "/workspace/bin"},
	}, cfg, dir)
	require.NoError(t, err)

	_, statErr := os.Stat(filepath.Join(dir, ".devm"))
	require.True(t, os.IsNotExist(statErr),
		"ApplyLive must NOT write .devm/ to the workspace; got: %v", statErr)
	assert.Equal(t, 1, countCalls(t, log, "exec -i"), "path-only change must still pipe a bundle")
}

func TestApplyLive_NoEnvOrTemplateChange_DoesNotPipeBundle(t *testing.T) {
	dir := t.TempDir()
	tr, log := fakeTartForApplyLive(t, dir)
	err := ApplyLive(tr, "x", []Change{
		{Kind: KindInstallChange},
	}, schema.Config{}, dir)
	require.NoError(t, err)
	assert.Equal(t, 0, countCalls(t, log, "exec -i"), "apply_live should not pipe a bundle when there's no env or template change")
	_, statErr := os.Stat(filepath.Join(dir, ".devm"))
	assert.True(t, os.IsNotExist(statErr), "apply_live should not touch the workspace when there's no env or template change")
}

func TestApplyLive_TemplateChange_PipesBundleThenInvokesDispatcher(t *testing.T) {
	dir := t.TempDir()
	// Provide the source template file so devmbundle.Build's render step
	// (RenderTemplates reads sources relative to repoRoot) succeeds.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "foo.tmpl"), []byte("hello\n"), 0o644))

	cfg := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p"},
		Services: map[string]schema.Service{
			"web": {Port: 80, Templates: []schema.Template{{Source: "foo.tmpl", Output: "/etc/foo"}}},
			"api": {Port: 81, Templates: []schema.Template{{Source: "foo.tmpl", Output: "/etc/bar"}}},
		},
	}

	tr, log := fakeTartForApplyLive(t, dir)

	changes := []Change{
		{Kind: KindTemplateChange, Service: "web", Detail: "/etc/foo", New: "installed"},
		{Kind: KindTemplateChange, Service: "api", Detail: "/etc/bar", New: "installed"},
	}
	assert.NoError(t, ApplyLive(tr, "x-sbx", changes, cfg, dir))

	// Bundle piped exactly once regardless of how many templates changed...
	assert.Equal(t, 1, countCalls(t, log, "exec -i"), "expected exactly one bundle pipe")
	// ...followed by exactly one dispatcher invocation.
	assert.Equal(t, 1, countCalls(t, log, "install-templates.sh"), "expected exactly one dispatcher invocation")

	// No host-side .devm/ writes — templates are packed into the bundle,
	// not written to the workspace.
	_, statErr := os.Stat(filepath.Join(dir, ".devm"))
	assert.True(t, os.IsNotExist(statErr), "ApplyLive must NOT write .devm/ to the workspace; got: %v", statErr)
}
