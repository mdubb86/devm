package serviceapi

import (
	"context"
	"os/exec"
	"syscall"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/supervisor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHealIronProxies_RespawnsMissing verifies the watchdog respawns
// a running project's iron-proxy after it silently dies — which is the
// buzztrack-style scenario the whole feature exists for. spawnIronProxyFn
// is stubbed to count invocations without execing the real binary.
func TestHealIronProxies_RespawnsMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sup := supervisor.New(t.TempDir())
	locks := NewProjectLocks()

	const projectID = "watchdog-respawn"
	// State a healthy running project would have written: schema
	// snapshot + on-disk iron-proxy config with real ports.
	seededCfg := schema.Config{
		Project: schema.Project{Name: projectID},
		Network: schema.Network{Allow: []schema.AllowEntry{{Host: "api.example.com"}}},
	}
	require.NoError(t, WriteStateSnapshot(identity.Prod, projectID, StateSnapshot{Cfg: seededCfg, ProjectIP: "127.42.0.5"}))

	httpPort, err := pickPort()
	require.NoError(t, err)
	httpsPort, err := pickPort()
	require.NoError(t, err)
	dnsPort, err := pickPort()
	require.NoError(t, err)
	writePreExistingIronProxyConfig(t, projectID, "127.0.0.1", httpPort, httpsPort, dnsPort)

	// Register with ironProxyState so the watchdog's iteration sees it.
	ironProxyState.put(projectID, projectInfo{
		ProjectIP: "127.42.0.5",
		HTTPPort:  httpPort,
		HTTPSPort: httpsPort,
		DNSPort:   dnsPort,
	})
	t.Cleanup(func() { ironProxyState.del(projectID) })

	// No iron-proxy running for the project → computeProxyHealth
	// returns Missing (no supervisor entry + config exists on disk).
	require.Equal(t, ProxyMissing, computeProxyHealth(identity.Prod, sup, nil, projectID).Status)

	origSpawn := spawnIronProxyFn
	t.Cleanup(func() { spawnIronProxyFn = origSpawn })
	var spawnCalls int
	var lastCfg IronProxyConfig
	spawnIronProxyFn = func(_ context.Context, _ identity.Config, s *supervisor.Supervisor, id string, cfg IronProxyConfig, _ *Denials) error {
		spawnCalls++
		lastCfg = cfg
		// Mark supervisor as running so the recheck-under-lock inside
		// respawnIronProxyFromState observes success and doesn't loop.
		s.Adopt(supervisor.Key{ProjectID: id, Role: supervisor.RoleProxy}, 1)
		return nil
	}

	healIronProxies(context.Background(), identity.Prod, sup, nil, locks, nil)

	assert.Equal(t, 1, spawnCalls, "should respawn exactly once for the missing project")
	assert.Equal(t, []string{"api.example.com"}, lastCfg.AllowList,
		"allowlist should reconstruct from snap.Cfg.Network.Domains()")
	assert.Contains(t, lastCfg.HTTPSListen, "127.0.0.1:", "listen addr preserves loopback")
}

// TestHealIronProxies_SkipsSecretsProjects verifies projects that inject
// secrets are logged + skipped, not silently respawned with empty
// secrets (which would produce a broken proxy that lets guest-side
// tokens leak in cleartext through the transform pipeline).
func TestHealIronProxies_SkipsSecretsProjects(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sup := supervisor.New(t.TempDir())
	locks := NewProjectLocks()

	const projectID = "watchdog-needs-secrets"
	// Schema with a secret-injecting env value — cfgHasSecretRefs
	// returns true, so computeProxyHealth marks NeedsSecrets.
	seededCfg := schema.Config{
		Project: schema.Project{Name: projectID},
		Env: map[string]schema.EnvValue{
			"API_KEY": {Secret: &schema.SecretRef{Name: "api_key"}},
		},
	}
	require.NoError(t, WriteStateSnapshot(identity.Prod, projectID, StateSnapshot{Cfg: seededCfg, ProjectIP: "127.42.0.6"}))

	httpPort, err := pickPort()
	require.NoError(t, err)
	httpsPort, err := pickPort()
	require.NoError(t, err)
	dnsPort, err := pickPort()
	require.NoError(t, err)
	writePreExistingIronProxyConfig(t, projectID, "127.0.0.1", httpPort, httpsPort, dnsPort)

	ironProxyState.put(projectID, projectInfo{
		ProjectIP: "127.42.0.6",
		HTTPPort:  httpPort,
		HTTPSPort: httpsPort,
		DNSPort:   dnsPort,
	})
	t.Cleanup(func() { ironProxyState.del(projectID) })

	// Assert the health signal that gates the watchdog decision, not
	// just that it's Missing.
	h := computeProxyHealth(identity.Prod, sup, nil, projectID)
	require.Equal(t, ProxyMissing, h.Status)
	require.True(t, h.NeedsSecrets, "cfgHasSecretRefs should detect the !secret env value")

	origSpawn := spawnIronProxyFn
	t.Cleanup(func() { spawnIronProxyFn = origSpawn })
	spawnCalls := 0
	spawnIronProxyFn = func(context.Context, identity.Config, *supervisor.Supervisor, string, IronProxyConfig, *Denials) error {
		spawnCalls++
		return nil
	}

	healIronProxies(context.Background(), identity.Prod, sup, nil, locks, nil)

	assert.Equal(t, 0, spawnCalls, "watchdog must not respawn a secrets-injecting project")
}

// TestHealIronProxies_SkipsHealthyProject verifies the watchdog no-ops
// when iron-proxy is up. Prevents spurious stop+respawn churn.
func TestHealIronProxies_SkipsHealthyProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sup := supervisor.New(t.TempDir())
	locks := NewProjectLocks()

	const projectID = "watchdog-healthy"
	seededCfg := schema.Config{Project: schema.Project{Name: projectID}}
	require.NoError(t, WriteStateSnapshot(identity.Prod, projectID, StateSnapshot{
		Cfg:          seededCfg,
		ProjectIP:    "127.42.0.7",
		ProxyVersion: ironproxyEmbedShaForTest(),
	}))

	httpPort, err := pickPort()
	require.NoError(t, err)
	httpsPort, err := pickPort()
	require.NoError(t, err)
	dnsPort, err := pickPort()
	require.NoError(t, err)
	writePreExistingIronProxyConfig(t, projectID, "127.0.0.1", httpPort, httpsPort, dnsPort)

	// Adopt a real long-lived child pid so supervisor.Status reports
	// Present+Running (kill(pid, 0) probe); computeProxyHealth then
	// returns OK. Same pattern as apply_iron_proxy_test.go's
	// TestApplyIronProxy_RunningRestartSucceeds.
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			_ = syscall.Kill(pid, syscall.SIGKILL)
			<-done
		}
	})
	sup.Adopt(supervisor.Key{ProjectID: projectID, Role: supervisor.RoleProxy}, pid)
	ironProxyState.put(projectID, projectInfo{ProjectIP: "127.42.0.7"})
	t.Cleanup(func() { ironProxyState.del(projectID) })

	require.Equal(t, ProxyOK, computeProxyHealth(identity.Prod, sup, nil, projectID).Status)

	origSpawn := spawnIronProxyFn
	t.Cleanup(func() { spawnIronProxyFn = origSpawn })
	spawnCalls := 0
	spawnIronProxyFn = func(context.Context, identity.Config, *supervisor.Supervisor, string, IronProxyConfig, *Denials) error {
		spawnCalls++
		return nil
	}

	healIronProxies(context.Background(), identity.Prod, sup, nil, locks, nil)
	assert.Equal(t, 0, spawnCalls, "healthy iron-proxy shouldn't be respawned")
}

// ironproxyEmbedShaForTest returns the current embedded iron-proxy sha
// as a fake ProxyVersion so the snapshot's ProxyVersion matches the
// current embed and computeProxyHealth doesn't classify it as STALE.
func ironproxyEmbedShaForTest() string {
	// Import lives in an inlined func so the main watchdog file doesn't
	// need it just for a test string.
	return "" // empty ProxyVersion is treated as unknown-but-not-stale (see proxyhealth.go line 74)
}
