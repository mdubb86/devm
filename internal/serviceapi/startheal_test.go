package serviceapi

import (
	"context"
	"strconv"
	"testing"

	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/supervisor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixedTartList reports a fixed set of VMs without shelling out to
// `tart`. Unlike reconcile_test.go's fakeTartList (one VM only), this
// heal pass needs to report several VMs running at once.
type fixedTartList struct {
	vms []tart.VM
}

func (f *fixedTartList) List(ctx context.Context) ([]tart.VM, error) {
	return f.vms, nil
}

// TestHealNoSecretProxiesAtStartup_SpawnsForMissingNoSecretProxy covers
// the core respawn path: a no-secret project with its VM running and no
// live iron-proxy (config file on disk, but nothing supervised) gets a
// fresh spawn using the ports recovered from that config file, and the
// snapshot's ProxyVersion is stamped afterward. A sibling project that
// injects secrets must be left alone even though its VM is also running
// and it also has no live proxy.
func TestHealNoSecretProxiesAtStartup_SpawnsForMissingNoSecretProxy(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())
	sup := supervisor.New(t.TempDir())

	// Project "p": no secret refs, VM running, iron-proxy config exists
	// on disk (so ports resolve) but nothing supervised -> MISSING.
	require.NoError(t, WriteStateSnapshot("p", StateSnapshot{
		Cfg: schema.Config{Project: schema.Project{ID: "p", VMName: "p-vm"}},
	}))
	macHost := "127.0.0.1"
	httpPort, err := pickPort()
	require.NoError(t, err)
	httpsPort, err := pickPort()
	require.NoError(t, err)
	dnsPort, err := pickPort()
	require.NoError(t, err)
	writePreExistingIronProxyConfig(t, "p", macHost, httpPort, httpsPort, dnsPort)

	// Project "s": has a secret ref, VM running, no config file at all
	// (also MISSING) -> must be skipped because it needs the CLI's
	// keychain access, not because of anything else.
	require.NoError(t, WriteStateSnapshot("s", StateSnapshot{
		Cfg: schema.Config{
			Project: schema.Project{ID: "s", VMName: "s-vm"},
			Env: map[string]schema.EnvValue{
				"TOKEN": {Secret: &schema.SecretRef{Name: "token"}},
			},
		},
	}))

	tr := &fixedTartList{vms: []tart.VM{
		{Name: "p-vm", Running: true},
		{Name: "s-vm", Running: true},
	}}

	origSpawn := spawnIronProxyFn
	t.Cleanup(func() { spawnIronProxyFn = origSpawn })
	var spawned []string
	var spawnedCfg IronProxyConfig
	spawnIronProxyFn = func(_ context.Context, _ *supervisor.Supervisor, projectID string, cfg IronProxyConfig, _ *Denials) error {
		spawned = append(spawned, projectID)
		if projectID == "p" {
			spawnedCfg = cfg
		}
		return nil
	}

	healNoSecretProxiesAtStartup(context.Background(), tr, sup, NewDenials())

	assert.Contains(t, spawned, "p")
	assert.NotContains(t, spawned, "s", "secret-bearing project must not be healed daemon-side")

	assert.Equal(t, "127.0.0.1:"+strconv.Itoa(httpPort), spawnedCfg.HTTPListen)
	assert.Equal(t, "127.0.0.1:"+strconv.Itoa(httpsPort), spawnedCfg.HTTPSListen)
	assert.Equal(t, "127.0.0.1:"+strconv.Itoa(dnsPort), spawnedCfg.DNSListen)

	snap, err := ReadStateSnapshot("p")
	require.NoError(t, err)
	require.NotNil(t, snap)
	assert.NotEmpty(t, snap.ProxyVersion, "stampProxyVersion should have set ProxyVersion")

	// "s" was skipped entirely -> its snapshot is untouched.
	sSnap, err := ReadStateSnapshot("s")
	require.NoError(t, err)
	require.NotNil(t, sSnap)
	assert.Empty(t, sSnap.ProxyVersion)
}

// TestHealNoSecretProxiesAtStartup_SkipsWhenProxyAlreadyHealthy covers
// the "nothing to do" path: a no-secret project whose VM is running and
// whose iron-proxy is already OK (supervised + live + config current)
// must not be respawned — a startup heal that respawned healthy proxies
// unconditionally would evict live connections on every daemon restart.
func TestHealNoSecretProxiesAtStartup_SkipsWhenProxyAlreadyHealthy(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())
	sup := healthyIronProxySupervisor(t, "p")

	require.NoError(t, WriteStateSnapshot("p", StateSnapshot{
		Cfg: schema.Config{Project: schema.Project{ID: "p", VMName: "p-vm"}},
	}))
	macHost := "127.0.0.1"
	httpPort, err := pickPort()
	require.NoError(t, err)
	httpsPort, err := pickPort()
	require.NoError(t, err)
	dnsPort, err := pickPort()
	require.NoError(t, err)
	writePreExistingIronProxyConfig(t, "p", macHost, httpPort, httpsPort, dnsPort)

	tr := &fixedTartList{vms: []tart.VM{{Name: "p-vm", Running: true}}}

	origSpawn := spawnIronProxyFn
	t.Cleanup(func() { spawnIronProxyFn = origSpawn })
	var spawned []string
	spawnIronProxyFn = func(_ context.Context, _ *supervisor.Supervisor, projectID string, _ IronProxyConfig, _ *Denials) error {
		spawned = append(spawned, projectID)
		return nil
	}

	healNoSecretProxiesAtStartup(context.Background(), tr, sup, NewDenials())

	assert.Empty(t, spawned, "an already-healthy proxy must not be respawned")
}
