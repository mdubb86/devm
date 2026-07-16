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
	stdinLog := stdinLogPath(log)
	bin := filepath.Join(dir, "fake-tart")
	// argv goes to log (unchanged shape — countCalls' line-based counting
	// depends on this file containing ONLY argv lines). Any piped stdin
	// (binary bundle tars, the svc_ingress nft script, etc.) is dumped to
	// a SEPARATE file — mixing it into log would let a coincidental
	// substring match inside a bundle's tar payload (e.g. an embedded
	// filename like "install-templates.sh") corrupt countCalls' counts.
	script := "#!/bin/sh\necho \"$*\" >> " + log + "\ncat >> " + stdinLog + "\nexec true\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = bin
	return tr, log
}

// stdinLogPath derives the sibling file fakeTartForApplyLive dumps piped
// stdin bodies into, given the argv log path it returned.
func stdinLogPath(log string) string {
	return log + ".stdin"
}

// fakeTartForApplyLiveFailingOn is fakeTartForApplyLive's evil twin: it
// still records argv and dumps piped stdin the same way, but exits 1
// instead of 0 whenever the piped stdin body contains failMarker. Used
// to simulate a mid-script failure (e.g. `sudo nft -f -` rejecting a
// rule) and assert that ApplyLive's `if r.ExitCode != 0 { return err }`
// check actually sees it — i.e. the exit code the guest shell reports
// back is what ApplyLive's error path keys on. This does NOT exercise
// bash -e/-o pipefail semantics themselves (there's no real bash/nft
// inside this fake); that inner-shell errexit behavior is covered by
// the e2e suite against a real guest. It only pins that ApplyLive
// propagates a nonzero ExecStdin exit as an error, which is the other
// half of the fix.
func fakeTartForApplyLiveFailingOn(t *testing.T, dir, failMarker string) (*tart.Tart, string) {
	t.Helper()
	log := filepath.Join(dir, "tart-calls.txt")
	stdinLog := stdinLogPath(log)
	bin := filepath.Join(dir, "fake-tart")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + log + "\n" +
		"body=$(cat)\n" +
		"printf '%s' \"$body\" >> " + stdinLog + "\n" +
		"case \"$body\" in\n" +
		"*" + failMarker + "*) exit 1 ;;\n" +
		"esac\n" +
		"exec true\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = bin
	return tr, log
}

// logContains reports whether any stdin body piped to the fake tart
// (e.g. the svc_ingress nft script delivered via ExecStdin) contains
// substr. logPath is the argv-log path returned by fakeTartForApplyLive.
func logContains(t *testing.T, logPath, substr string) bool {
	t.Helper()
	data, err := os.ReadFile(stdinLogPath(logPath))
	if os.IsNotExist(err) {
		return false
	}
	require.NoError(t, err)
	return strings.Contains(string(data), substr)
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
	}, schema.Config{}, dir, nil, nil, nil, nil)
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
	}, schema.Config{}, dir, nil, nil, nil, nil)
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
		Project: schema.Project{Name: "p"},
		Env:     map[string]schema.EnvValue{"FOO": {Literal: "new"}},
	}

	err := ApplyLive(tr, "p-vm", []Change{
		{Kind: KindEnvChange, Key: "FOO", Old: "old", New: "new"},
	}, cfg, dir, nil, nil, nil, nil)
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
		}, cfg, dir, nil, nil, nil, nil)
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
	}, cfg, dir, nil, nil, nil, nil)
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
	}, cfg, dir, nil, nil, nil, nil)
	require.NoError(t, err)

	_, statErr := os.Stat(filepath.Join(dir, ".devm"))
	require.True(t, os.IsNotExist(statErr),
		"ApplyLive must NOT write .devm/ to the workspace; got: %v", statErr)
	assert.Equal(t, 1, countCalls(t, log, "exec -i"), "path-only change must still pipe a bundle")
}

// TestApplyLive_StartupChange_PipesBundle_NoWorkspaceWrite pins the
// mechanism KindStartupChange uses to reach the running VM: same as env
// and path changes, it rebuilds the devmbundle (which re-renders
// devm-startup.service from cfg.Startup) and pipes it into the guest —
// no host workspace write. The unit itself only runs at boot, so this
// doesn't restart anything; it just makes the new content available for
// the VM's next cold start.
func TestApplyLive_StartupChange_PipesBundle_NoWorkspaceWrite(t *testing.T) {
	dir := t.TempDir()
	tr, log := fakeTartForApplyLive(t, dir)
	cfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Startup: []string{"echo one", "echo two"},
	}

	err := ApplyLive(tr, "p-vm", []Change{
		{Kind: KindStartupChange},
	}, cfg, dir, nil, nil, nil, nil)
	require.NoError(t, err)

	_, statErr := os.Stat(filepath.Join(dir, ".devm"))
	require.True(t, os.IsNotExist(statErr),
		"ApplyLive must NOT write .devm/ to the workspace; got: %v", statErr)
	assert.Equal(t, 1, countCalls(t, log, "exec -i"), "startup change must pipe a bundle")
}

func TestApplyLive_NoEnvOrTemplateChange_DoesNotPipeBundle(t *testing.T) {
	dir := t.TempDir()
	tr, log := fakeTartForApplyLive(t, dir)
	err := ApplyLive(tr, "x", []Change{
		{Kind: KindInstallChange},
	}, schema.Config{}, dir, nil, nil, nil, nil)
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
		Project: schema.Project{Name: "p"},
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
	assert.NoError(t, ApplyLive(tr, "x-sbx", changes, cfg, dir, nil, nil, nil, nil))

	// Bundle piped exactly once regardless of how many templates changed...
	assert.Equal(t, 1, countCalls(t, log, "exec -i"), "expected exactly one bundle pipe")
	// ...followed by exactly one dispatcher invocation.
	assert.Equal(t, 1, countCalls(t, log, "install-templates.sh"), "expected exactly one dispatcher invocation")

	// No host-side .devm/ writes — templates are packed into the bundle,
	// not written to the workspace.
	_, statErr := os.Stat(filepath.Join(dir, ".devm"))
	assert.True(t, os.IsNotExist(statErr), "ApplyLive must NOT write .devm/ to the workspace; got: %v", statErr)
}

// TestApplyLiveRebuildsSvcIngress is a regression test for Task 9: a
// KindServiceDirectChange in the change set must flush-rebuild the
// svc_ingress chain live via ExecStdin, from the CURRENT cfg's direct
// ports — the single source of truth shared with the provisioner
// (internal/nftscript.BuildSvcIngressScript + DirectPorts).
func TestApplyLiveRebuildsSvcIngress(t *testing.T) {
	dir := t.TempDir()
	tr, log := fakeTartForApplyLive(t, dir)
	cfg := schema.Config{
		Docker:  true,
		Project: schema.Project{Name: "p"},
		Services: map[string]schema.Service{
			"db": {Port: 54322, Hostname: "db.test", Direct: true},
		},
	}
	changes := []Change{{Kind: KindServiceDirectChange, Service: "db"}}
	require.NoError(t, ApplyLive(tr, "p", changes, cfg, dir, nil, nil, nil, nil))
	assert.True(t, logContains(t, log, "flush chain inet devm_filter svc_ingress"))
	assert.True(t, logContains(t, log, "ct original proto-dst 54322 accept"))
}

// TestApplyLiveRebuildsSvcIngress_NonDockerStillClosesChain pins the
// no-explicit-gate behavior: DirectPorts returns nil for non-docker
// projects, so a KindServiceDirectChange still fires the rebuild — it
// just flushes to empty, closing any stale direct ingress rather than
// leaving it open.
func TestApplyLiveRebuildsSvcIngress_NonDockerStillClosesChain(t *testing.T) {
	dir := t.TempDir()
	tr, log := fakeTartForApplyLive(t, dir)
	cfg := schema.Config{
		Docker:  false,
		Project: schema.Project{Name: "p"},
		Services: map[string]schema.Service{
			"db": {Port: 54322, Hostname: "db.test", Direct: true},
		},
	}
	changes := []Change{{Kind: KindServiceDirectChange, Service: "db"}}
	require.NoError(t, ApplyLive(tr, "p", changes, cfg, dir, nil, nil, nil, nil))
	assert.True(t, logContains(t, log, "flush chain inet devm_filter svc_ingress"))
	assert.False(t, logContains(t, log, "ct original proto-dst 54322 accept"))
}

// TestApplyLiveRebuildsSvcIngress_FailurePropagates is a regression
// test for the code-review fix to Task 9: a nonzero exit from the
// svc_ingress rebuild's ExecStdin call must surface as a non-nil error
// from ApplyLive, not be silently swallowed. Before the fix, the
// rebuild ran as `bash -e -o pipefail -c "cat | sudo bash"` — errexit
// applied to the OUTER bash, but the nft script was interpreted by the
// INNER, errexit-less `sudo bash`, so a failing mid-script command
// (e.g. `nft -f -` rejecting a rule) didn't abort the script and the
// reported exit code was just the last line's (a `list chain` snapshot
// that typically succeeds regardless) — ApplyLive returned nil despite
// a broken chain. This test doesn't exercise real bash/nft errexit
// semantics (see fakeTartForApplyLiveFailingOn's doc comment); it pins
// the other half: that ApplyLive actually returns an error when the
// exit code IS nonzero, which is what the errexit fix now makes
// possible for real mid-script nft failures.
func TestApplyLiveRebuildsSvcIngress_FailurePropagates(t *testing.T) {
	dir := t.TempDir()
	tr, _ := fakeTartForApplyLiveFailingOn(t, dir, "flush chain inet devm_filter svc_ingress")
	cfg := schema.Config{
		Docker:  true,
		Project: schema.Project{Name: "p"},
		Services: map[string]schema.Service{
			"db": {Port: 54322, Hostname: "db.test", Direct: true},
		},
	}
	changes := []Change{{Kind: KindServiceDirectChange, Service: "db"}}
	err := ApplyLive(tr, "p", changes, cfg, dir, nil, nil, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "svc_ingress")
}

// TestApplyLive_NoDirectChange_DoesNotTouchSvcIngress ensures the
// rebuild is gated strictly on KindServiceDirectChange being present —
// unrelated change kinds must not fire an nft rebuild.
func TestApplyLive_NoDirectChange_DoesNotTouchSvcIngress(t *testing.T) {
	dir := t.TempDir()
	tr, log := fakeTartForApplyLive(t, dir)
	cfg := schema.Config{Docker: true}
	err := ApplyLive(tr, "x", []Change{
		{Kind: KindInstallChange},
	}, cfg, dir, nil, nil, nil, nil)
	require.NoError(t, err)
	assert.False(t, logContains(t, log, "flush chain inet devm_filter svc_ingress"))
}
