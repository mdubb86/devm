package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mtwaage/devm/internal/sandbox"
	"github.com/mtwaage/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyLive_PortAdd(t *testing.T) {
	r := &stubRunner{}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}
	err := ApplyLive(sb, []Change{
		{Kind: KindPortAdd, Service: "api", Key: "8080", New: "8080"},
	}, 50000, schema.Config{}, t.TempDir())
	assert.NoError(t, err)
	cmd := strings.Join(r.lastArgs[0], " ")
	// Explicit 127.0.0.1: prefix — see publishSpec in ports.go for why
	// we don't use the bare HOST:SANDBOX form anymore.
	assert.Contains(t, cmd, "sbx ports x --publish 127.0.0.1:58080:8080")
}

func TestApplyLive_PortRemove(t *testing.T) {
	r := &stubRunner{}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}
	err := ApplyLive(sb, []Change{
		{Kind: KindPortRemove, Service: "api", Key: "8080", Old: "8080"},
	}, 50000, schema.Config{}, t.TempDir())
	assert.NoError(t, err)
	cmd := strings.Join(r.lastArgs[0], " ")
	assert.Contains(t, cmd, "sbx ports x --unpublish 127.0.0.1:58080:8080")
}

func TestApplyLive_PortChange(t *testing.T) {
	r := &stubRunner{}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}
	err := ApplyLive(sb, []Change{
		{Kind: KindPortChange, Service: "api", Key: "9090", Old: "8080", New: "9090"},
	}, 50000, schema.Config{}, t.TempDir())
	assert.NoError(t, err)
	assert.Len(t, r.lastArgs, 2, "port_change should be 2 calls: unpublish then publish")
	c0 := strings.Join(r.lastArgs[0], " ")
	c1 := strings.Join(r.lastArgs[1], " ")
	assert.Contains(t, c0, "--unpublish 127.0.0.1:58080:8080")
	assert.Contains(t, c1, "--publish 127.0.0.1:59090:9090")
}

func TestApplyLive_NetworkAdd(t *testing.T) {
	r := &stubRunner{}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}
	err := ApplyLive(sb, []Change{
		{Kind: KindNetworkAdd, Key: "newdomain.example.com", New: "newdomain.example.com"},
	}, 50000, schema.Config{}, t.TempDir())
	assert.NoError(t, err)
	cmd := strings.Join(r.lastArgs[0], " ")
	// sbx 0.29+ requires scope: SANDBOX before RESOURCES. devm uses
	// the sandbox name (per-project network policy).
	assert.Contains(t, cmd, "sbx policy allow network x newdomain.example.com")
}

func TestApplyLive_SkipsRecreateKinds(t *testing.T) {
	r := &stubRunner{}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}
	err := ApplyLive(sb, []Change{
		{Kind: KindEnvChange, Service: "api", Key: "X", Old: "a", New: "b"},
		{Kind: KindInstallChange},
		{Kind: KindNetworkRemove, Key: "gone.com", Old: "gone.com"},
	}, 50000, schema.Config{}, t.TempDir())
	assert.NoError(t, err)
	assert.Empty(t, r.lastArgs, "non-LIVE changes must be ignored by ApplyLive")
}

func TestApplyLive_TemplateChange_InvokesDispatcher(t *testing.T) {
	dir := t.TempDir()
	// Provide the source template file so WriteTemplateInstallers succeeds.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "foo.tmpl"), []byte("hello\n"), 0o644))

	cfg := schema.Config{
		Project: schema.Project{ID: "p", SandboxName: "p", HostnameApex: "p.local"},
		Services: map[string]schema.Service{
			"web": {Port: 80, Templates: []schema.Template{{Source: "foo.tmpl", Output: "/etc/foo"}}},
			"api": {Port: 81, Templates: []schema.Template{{Source: "foo.tmpl", Output: "/etc/bar"}}},
		},
	}

	r := &stubRunner{}
	sb := &sandbox.Sandbox{Name: "x-sbx", Runner: r}

	changes := []Change{
		{Kind: KindTemplateChange, Service: "web", Detail: "/etc/foo", New: "installed"},
		{Kind: KindTemplateChange, Service: "api", Detail: "/etc/bar", New: "installed"},
	}
	assert.NoError(t, ApplyLive(sb, changes, 50000, cfg, dir))

	// One single sbx exec invocation regardless of how many templates changed.
	dispatchCalls := 0
	for _, args := range r.lastArgs {
		c := strings.Join(args, " ")
		if strings.Contains(c, "install-templates.sh") {
			dispatchCalls++
		}
	}
	assert.Equal(t, 1, dispatchCalls, "expected exactly one dispatcher invocation; saw lastArgs: %v", r.lastArgs)
}
