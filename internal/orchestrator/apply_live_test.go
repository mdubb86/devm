package orchestrator

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
// that records its invocations and exits 0.
func fakeTartForApplyLive(t *testing.T, dir string) (*tart.Tart, *[][]string) {
	t.Helper()
	calls := &[][]string{}
	log := filepath.Join(dir, "tart-calls.txt")
	bin := filepath.Join(dir, "fake-tart")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\nexec true\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = bin
	_ = calls
	return tr, calls
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

func TestApplyLive_NetworkKindsAreNoOps(t *testing.T) {
	// Network egress is Ship 5 (iron-proxy); no apply path in Ship 4.
	dir := t.TempDir()
	tr, _ := fakeTartForApplyLive(t, dir)
	err := ApplyLive(tr, "x", []Change{
		{Kind: KindNetworkAdd, Key: "example.com", New: "example.com"},
		{Kind: KindNetworkRemove, Key: "old.example.com", Old: "old.example.com"},
	}, schema.Config{}, dir)
	assert.NoError(t, err)
}

func TestApplyLive_EnvChange_WritesDevmEnv(t *testing.T) {
	dir := t.TempDir()
	tr, _ := fakeTartForApplyLive(t, dir)
	cfg := schema.Config{Env: map[string]string{"FOO": "bar"}}

	err := ApplyLive(tr, "x", []Change{
		{Kind: KindEnvChange, Key: "FOO", Old: "old", New: "bar"},
	}, cfg, dir)
	require.NoError(t, err)

	bs, err := os.ReadFile(filepath.Join(dir, ".devm", ".env"))
	require.NoError(t, err)
	assert.Contains(t, string(bs), `export FOO='bar'`)
}

func TestApplyLive_EnvAddAndRemove_AlsoWriteDevmEnv(t *testing.T) {
	for _, kind := range []ChangeKind{KindEnvAdd, KindEnvRemove} {
		dir := t.TempDir()
		tr, _ := fakeTartForApplyLive(t, dir)
		cfg := schema.Config{Env: map[string]string{"K": "v"}}
		err := ApplyLive(tr, "x", []Change{
			{Kind: kind, Key: "K", New: "v"},
		}, cfg, dir)
		require.NoError(t, err, "kind=%v", kind)
		_, err = os.Stat(filepath.Join(dir, ".devm", ".env"))
		require.NoError(t, err, ".devm/.env must be written for kind=%v", kind)
	}
}

func TestApplyLive_MultipleEnvChanges_SingleWrite(t *testing.T) {
	dir := t.TempDir()
	tr, _ := fakeTartForApplyLive(t, dir)
	cfg := schema.Config{Env: map[string]string{"A": "1", "B": "2", "C": "3"}}
	err := ApplyLive(tr, "x", []Change{
		{Kind: KindEnvAdd, Key: "A", New: "1"},
		{Kind: KindEnvChange, Key: "B", Old: "x", New: "2"},
		{Kind: KindEnvAdd, Key: "C", New: "3"},
	}, cfg, dir)
	require.NoError(t, err)
	bs, err := os.ReadFile(filepath.Join(dir, ".devm", ".env"))
	require.NoError(t, err)
	assert.Contains(t, string(bs), `export A='1'`)
	assert.Contains(t, string(bs), `export B='2'`)
	assert.Contains(t, string(bs), `export C='3'`)
}

func TestApplyLive_NoEnvChange_DoesNotWriteDevmEnv(t *testing.T) {
	dir := t.TempDir()
	tr, _ := fakeTartForApplyLive(t, dir)
	err := ApplyLive(tr, "x", []Change{
		{Kind: KindInstallChange},
	}, schema.Config{}, dir)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, ".devm", ".env"))
	assert.True(t, os.IsNotExist(err), "apply_live should not touch .devm/.env when there's no env change")
}

func TestApplyLive_TemplateChange_InvokesDispatcher(t *testing.T) {
	dir := t.TempDir()
	// Provide the source template file so WriteTemplateInstallers succeeds.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "foo.tmpl"), []byte("hello\n"), 0o644))

	cfg := schema.Config{
		Project: schema.Project{ID: "p", SandboxName: "p"},
		Services: map[string]schema.Service{
			"web": {Port: 80, Templates: []schema.Template{{Source: "foo.tmpl", Output: "/etc/foo"}}},
			"api": {Port: 81, Templates: []schema.Template{{Source: "foo.tmpl", Output: "/etc/bar"}}},
		},
	}

	// Build a fake tart binary that records its argv calls.
	callLog := filepath.Join(dir, "calls.txt")
	bin := filepath.Join(dir, "fake-tart")
	script := "#!/bin/sh\necho \"$*\" >> " + callLog + "\nexec true\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = bin

	changes := []Change{
		{Kind: KindTemplateChange, Service: "web", Detail: "/etc/foo", New: "installed"},
		{Kind: KindTemplateChange, Service: "api", Detail: "/etc/bar", New: "installed"},
	}
	assert.NoError(t, ApplyLive(tr, "x-sbx", changes, cfg, dir))

	// One single tart exec invocation for the dispatcher regardless of
	// how many templates changed.
	logged, err := os.ReadFile(callLog)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(logged)), "\n")
	dispatchCalls := 0
	for _, line := range lines {
		if strings.Contains(line, "install-templates.sh") {
			dispatchCalls++
		}
	}
	assert.Equal(t, 1, dispatchCalls, "expected exactly one dispatcher invocation; got calls: %v", lines)
}
