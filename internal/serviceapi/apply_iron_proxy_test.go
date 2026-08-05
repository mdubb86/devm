package serviceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/supervisor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTartIP returns a *tart.Tart whose `tart ip` always succeeds with
// ip. Stands in for a running VM.
func fakeTartIP(t *testing.T, ip string) *tart.Tart {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "tart-fake")
	script := "#!/bin/sh\necho " + ip + "\nexit 0\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = bin
	return tr
}

// fakeTartIPFails returns a *tart.Tart whose `tart ip` always fails —
// stands in for a VM that isn't actually running.
func fakeTartIPFails() *tart.Tart {
	tr := tart.New()
	tr.Path = "false"
	return tr
}

// writePreExistingIronProxyConfig drops a minimal YAML at the
// per-project path so /vm/apply-iron-proxy can pull ports out of it.
func writePreExistingIronProxyConfig(t *testing.T, projectID, macHost string, httpPort, httpsPort, dnsPort int) {
	t.Helper()
	path, err := IronProxyConfigPath(identity.Prod, projectID)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	body := []byte(
		"dns:\n" +
			"  listen: " + macHost + ":" + strconv.Itoa(dnsPort) + "\n" +
			"proxy:\n" +
			"  http_listen: " + macHost + ":" + strconv.Itoa(httpPort) + "\n" +
			"  https_listen: " + macHost + ":" + strconv.Itoa(httpsPort) + "\n",
	)
	require.NoError(t, os.WriteFile(path, body, 0o600))
}

func TestApplyIronProxy_VMStopped_NoConfigFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := NewServer(identity.Prod.SocketPath(), Build{})
	sup := supervisor.New(t.TempDir())
	RegisterApplyIronProxyHandler(srv, identity.Prod, NewProjectLocks(), sup, fakeTartIPFails(), nil, nil)

	// Simulate cold-start (`devm start` / `devm shell`) having already
	// seeded the snapshot with the real schema.Config — a prior
	// /vm/start ran for this project, but no iron-proxy config file has
	// been written yet (e.g. VM was stopped again before ever spawning
	// iron-proxy).
	seededCfg := schema.Config{Project: schema.Project{Name: "p"}}
	require.NoError(t, WriteStateSnapshot(identity.Prod, "p", StateSnapshot{Cfg: seededCfg}))

	// No config file exists → VM has never started iron-proxy. Snapshot
	// should still update; response signals no live apply.
	body, _ := json.Marshal(VMApplyIronProxyRequest{
		Name:      "p",
		Allowlist: []string{"a.example.com"},
		Secrets:   nil,
	})
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/apply-iron-proxy", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	var resp VMApplyIronProxyResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.False(t, resp.Applied)
	assert.False(t, resp.Revived)
	assert.False(t, resp.VMRunning)

	// Snapshot's SecretHashes must still update even with no live VM,
	// so the next /vm/start writes iron-proxy config from the current
	// schema without re-detecting the same drift. Likewise snap.Cfg
	// must fold in the applied allowlist, not stay frozen at the seeded
	// value — otherwise a future /vm/start would re-detect this same
	// allow-list change as pending drift.
	snap, err := ReadStateSnapshot(identity.Prod, "p")
	require.NoError(t, err)
	require.NotNil(t, snap)
	require.Len(t, snap.Cfg.Network.Allow, 1, "allow list must have merged the applied entry")
	assert.Equal(t, "a.example.com", snap.Cfg.Network.Allow[0].Host)
}

// TestApplyIronProxy_NeverColdStarted_FailsLoud covers F3: if
// apply-iron-proxy is invoked before any cold-start has ever seeded a
// snapshot for the project (no prior /vm/start), there is no real
// schema.Config available to preserve. Writing
// StateSnapshot{SecretHashes: hashes} with a zero-valued Cfg would make
// every field in the eventual real cfg look like a pending
// teardown-required change on the very next reconcile. The handler
// must fail loud instead of fabricating a snapshot.
func TestApplyIronProxy_NeverColdStarted_FailsLoud(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := NewServer(identity.Prod.SocketPath(), Build{})
	sup := supervisor.New(t.TempDir())
	RegisterApplyIronProxyHandler(srv, identity.Prod, NewProjectLocks(), sup, fakeTartIPFails(), nil, nil)

	body, _ := json.Marshal(VMApplyIronProxyRequest{
		Name:      "never-started",
		Allowlist: []string{"a.example.com"},
	})
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/apply-iron-proxy", bytes.NewReader(body)))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	snap, err := ReadStateSnapshot(identity.Prod, "never-started")
	require.NoError(t, err)
	assert.Nil(t, snap, "no snapshot should be fabricated on this failure path")
}

// TestApplyIronProxy_RunningRestartSucceeds covers the "iron-proxy was
// already running" happy path: a real config file exists on disk (so
// MAC_HOST:port is preserved), the supervisor reports the process as
// alive (simulated via Adopt on a real child pid, mirroring
// TestSupervisor_AdoptedStatusAndStop), and the handler must stop the
// old process, spawn a fresh one, verify it's listening, and persist
// SecretHashes. SpawnIronProxy itself is expensive (execs the real
// iron-proxy binary) so it's substituted via the spawnIronProxyFn
// injection seam with a stub that just opens the expected listener.
func TestApplyIronProxy_RunningRestartSucceeds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := NewServer(identity.Prod.SocketPath(), Build{})
	sup := supervisor.New(t.TempDir())

	const projectID = "p-running"
	// Simulate cold-start having already seeded the snapshot with the
	// real schema.Config; apply-iron-proxy requires this to exist (F3).
	seededCfg := schema.Config{Project: schema.Project{Name: projectID}}
	require.NoError(t, WriteStateSnapshot(identity.Prod, projectID, StateSnapshot{Cfg: seededCfg}))

	macHost := "127.0.0.1"
	httpPort, err := pickPort()
	require.NoError(t, err)
	httpsPort, err := pickPort()
	require.NoError(t, err)
	dnsPort, err := pickPort()
	require.NoError(t, err)
	writePreExistingIronProxyConfig(t, projectID, macHost, httpPort, httpsPort, dnsPort)

	// Simulate "iron-proxy already running" by adopting a real,
	// long-lived child process's pid — supervisor.Status only checks
	// liveness via kill(pid, 0), it doesn't care what the process is.
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			_ = syscall.Kill(pid, syscall.SIGKILL)
			<-done
		}
	})
	key := supervisor.Key{ProjectID: projectID, Role: supervisor.RoleProxy}
	sup.Adopt(key, pid)
	require.True(t, sup.Status(key).Present)
	require.True(t, sup.Status(key).Running)

	// Substitute the real SpawnIronProxy: instead of execing iron-proxy,
	// just bind the https listener the handler will health-check.
	origSpawn := spawnIronProxyFn
	t.Cleanup(func() { spawnIronProxyFn = origSpawn })
	var ln net.Listener
	spawnIronProxyFn = func(_ context.Context, _ identity.Config, _ *supervisor.Supervisor, _ string, proxyCfg IronProxyConfig, _ *Denials) error {
		var lerr error
		ln, lerr = net.Listen("tcp", proxyCfg.HTTPSListen)
		return lerr
	}

	t.Cleanup(func() { ironProxyState.del(projectID); ReleaseProjectIP(identity.Prod, projectID) })
	RegisterApplyIronProxyHandler(srv, identity.Prod, NewProjectLocks(), sup, fakeTartIP(t, "192.168.64.50"), nil, nil)

	reqBody, _ := json.Marshal(VMApplyIronProxyRequest{
		Name:      projectID,
		Allowlist: []string{"a.example.com"},
		Secrets: []SecretBinding{
			{Name: "github_token", Value: "s3cr3t", Hosts: []string{"api.github.com"}},
		},
	})
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/apply-iron-proxy", bytes.NewReader(reqBody)))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	if ln != nil {
		defer ln.Close()
	}

	var resp VMApplyIronProxyResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp.Applied)
	assert.False(t, resp.Revived, "was already running, so this is not a revival")
	assert.True(t, resp.VMRunning)

	snap, err := ReadStateSnapshot(identity.Prod, projectID)
	require.NoError(t, err)
	require.NotNil(t, snap)
	require.Contains(t, snap.SecretHashes, "github_token")
	// snap.Cfg must advance to reflect what apply-iron-proxy just wrote
	// to iron-proxy's config on disk. Without this, removals from allow
	// (or secret refs) are silently no-op'd on the next reconcile: diff
	// engine compares against a stale snap.Cfg and sees no change.
	require.Len(t, snap.Cfg.Network.Allow, 1, "allow list must have merged the applied entry")
	assert.Equal(t, "a.example.com", snap.Cfg.Network.Allow[0].Host)

	_, ok := ironProxyState.get(projectID)
	require.True(t, ok, "ironProxyState must hold an entry for the project after a successful apply")
}

// TestApplyIronProxy_PreservesProjectIP covers a reconcile-driven
// BucketEgressRestart apply (allowlist/secret drift) against a VM that's
// still running this daemon lifetime: ironProxyState already holds the
// project IP /vm/start allocated. The handler rebuilds `info` from
// iron-proxy's on-disk YAML config (loadIronProxyInfoFromConfig), which
// has no notion of ProjectIP — without carrying it forward from the
// pre-existing entry, this call would silently zero it out, and the
// next live reconcile's expose-map push would compute against no IP at
// all.
func TestApplyIronProxy_PreservesProjectIP(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := NewServer(identity.Prod.SocketPath(), Build{})
	sup := supervisor.New(t.TempDir())

	const projectID = "p-preserve-ip"
	t.Cleanup(func() { ironProxyState.del(projectID); ReleaseProjectIP(identity.Prod, projectID) })

	seededCfg := schema.Config{Project: schema.Project{Name: projectID}}
	require.NoError(t, WriteStateSnapshot(identity.Prod, projectID, StateSnapshot{Cfg: seededCfg, ProjectIP: "127.42.0.9"}))

	macHost := "127.0.0.1"
	httpPort, err := pickPort()
	require.NoError(t, err)
	httpsPort, err := pickPort()
	require.NoError(t, err)
	dnsPort, err := pickPort()
	require.NoError(t, err)
	writePreExistingIronProxyConfig(t, projectID, macHost, httpPort, httpsPort, dnsPort)

	// The VM is still running this daemon lifetime: ironProxyState
	// already holds the project IP /vm/start allocated.
	ironProxyState.put(projectID, projectInfo{ProjectIP: "127.42.0.9"})

	origSpawn := spawnIronProxyFn
	t.Cleanup(func() { spawnIronProxyFn = origSpawn })
	var ln net.Listener
	spawnIronProxyFn = func(_ context.Context, _ identity.Config, _ *supervisor.Supervisor, _ string, proxyCfg IronProxyConfig, _ *Denials) error {
		var lerr error
		ln, lerr = net.Listen("tcp", proxyCfg.HTTPSListen)
		return lerr
	}

	RegisterApplyIronProxyHandler(srv, identity.Prod, NewProjectLocks(), sup, fakeTartIPFails(), nil, nil)

	reqBody, _ := json.Marshal(VMApplyIronProxyRequest{
		Name:      projectID,
		Allowlist: []string{"a.example.com"},
	})
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/apply-iron-proxy", bytes.NewReader(reqBody)))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	if ln != nil {
		defer ln.Close()
	}

	info, ok := ironProxyState.get(projectID)
	require.True(t, ok)
	assert.Equal(t, "127.42.0.9", info.ProjectIP,
		"ProjectIP must be preserved across apply-iron-proxy, not zeroed")
}

// TestApplyIronProxy_AllocatesProjectIPWhenUnset covers adopt-in-place
// (internal/orchestrator/shell.go's "pristine: running but never
// provisioned" branch): a raw `tart run` or first-time adoption calls
// /vm/apply-iron-proxy directly, never /vm/start, so no project IP
// was ever allocated for this project this daemon lifetime. Without
// allocating one here, the adopted VM gets no ingress — no DNS
// resolution, no service listeners — until an explicit stop +
// cold-start.
func TestApplyIronProxy_AllocatesProjectIPWhenUnset(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := NewServer(identity.Prod.SocketPath(), Build{})
	sup := supervisor.New(t.TempDir())

	const projectID = "p-adopt-in-place"
	t.Cleanup(func() {
		ironProxyState.del(projectID)
		ReleaseProjectIP(identity.Prod, projectID)
	})

	seededCfg := schema.Config{
		Project: schema.Project{Name: projectID},
		Services: map[string]schema.Service{
			"db": {Port: 5432},
		},
	}
	require.NoError(t, WriteStateSnapshot(identity.Prod, projectID, StateSnapshot{Cfg: seededCfg}))

	macHost := "127.0.0.1"
	httpPort, err := pickPort()
	require.NoError(t, err)
	httpsPort, err := pickPort()
	require.NoError(t, err)
	dnsPort, err := pickPort()
	require.NoError(t, err)
	writePreExistingIronProxyConfig(t, projectID, macHost, httpPort, httpsPort, dnsPort)

	// Adopt-in-place: no prior ironProxyState entry for this project this
	// daemon lifetime — mirrors the state before /vm/apply-iron-proxy is
	// the first daemon call ever made for the adopted VM.

	origSpawn := spawnIronProxyFn
	t.Cleanup(func() { spawnIronProxyFn = origSpawn })
	var ln net.Listener
	spawnIronProxyFn = func(_ context.Context, _ identity.Config, _ *supervisor.Supervisor, _ string, proxyCfg IronProxyConfig, _ *Denials) error {
		var lerr error
		ln, lerr = net.Listen("tcp", proxyCfg.HTTPSListen)
		return lerr
	}

	RegisterApplyIronProxyHandler(srv, identity.Prod, NewProjectLocks(), sup, fakeTartIPFails(), nil, nil)

	reqBody, _ := json.Marshal(VMApplyIronProxyRequest{
		Name:      projectID,
		Allowlist: []string{"a.example.com"},
	})
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/apply-iron-proxy", bytes.NewReader(reqBody)))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	if ln != nil {
		defer ln.Close()
	}

	info, ok := ironProxyState.get(projectID)
	require.True(t, ok)
	assert.NotEmpty(t, info.ProjectIP,
		"apply-iron-proxy must allocate a project IP for an adopted VM that never went through /vm/start")
}

// TestApplyIronProxy_PreservesGuestOriginPorts covers F1: a
// reconcile-driven apply (allowlist/secret drift) against a project
// whose guest-origin listener pair was already started and stashed in
// ironProxyState (by a prior /vm/start or apply-iron-proxy call this
// daemon lifetime). loadIronProxyInfoFromConfig's on-disk YAML has no
// notion of GuestHTTPPort/GuestHTTPSPort — without carrying them
// forward from the pre-existing entry the way ProjectIP already is,
// this call would silently zero them, and the next warm `devm shell`'s
// endpoint push would carry dead guest ports.
func TestApplyIronProxy_PreservesGuestOriginPorts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := NewServer(identity.Prod.SocketPath(), Build{})
	sup := supervisor.New(t.TempDir())

	const projectID = "p-preserve-guest-ports"
	t.Cleanup(func() { ironProxyState.del(projectID); ReleaseProjectIP(identity.Prod, projectID) })

	seededCfg := schema.Config{Project: schema.Project{Name: projectID}}
	require.NoError(t, WriteStateSnapshot(identity.Prod, projectID, StateSnapshot{Cfg: seededCfg, ProjectIP: "127.42.0.9"}))

	macHost := "127.0.0.1"
	httpPort, err := pickPort()
	require.NoError(t, err)
	httpsPort, err := pickPort()
	require.NoError(t, err)
	dnsPort, err := pickPort()
	require.NoError(t, err)
	writePreExistingIronProxyConfig(t, projectID, macHost, httpPort, httpsPort, dnsPort)

	// Simulate a prior /vm/start (or apply-iron-proxy) having already
	// started and stashed this project's guest-origin listener pair.
	ironProxyState.put(projectID, projectInfo{ProjectIP: "127.42.0.9", GuestHTTPPort: 55001, GuestHTTPSPort: 55002})

	origSpawn := spawnIronProxyFn
	t.Cleanup(func() { spawnIronProxyFn = origSpawn })
	var ln net.Listener
	spawnIronProxyFn = func(_ context.Context, _ identity.Config, _ *supervisor.Supervisor, _ string, proxyCfg IronProxyConfig, _ *Denials) error {
		var lerr error
		ln, lerr = net.Listen("tcp", proxyCfg.HTTPSListen)
		return lerr
	}

	// proxy is nil here — this test pins the merge itself, independent
	// of whether a *ProxyServer is wired (F4 covers that separately).
	RegisterApplyIronProxyHandler(srv, identity.Prod, NewProjectLocks(), sup, fakeTartIPFails(), nil, nil)

	reqBody, _ := json.Marshal(VMApplyIronProxyRequest{
		Name:      projectID,
		Allowlist: []string{"a.example.com"},
	})
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/apply-iron-proxy", bytes.NewReader(reqBody)))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	if ln != nil {
		defer ln.Close()
	}

	info, ok := ironProxyState.get(projectID)
	require.True(t, ok)
	assert.Equal(t, 55001, info.GuestHTTPPort,
		"GuestHTTPPort must be preserved across apply-iron-proxy, not zeroed")
	assert.Equal(t, 55002, info.GuestHTTPSPort,
		"GuestHTTPSPort must be preserved across apply-iron-proxy, not zeroed")
}

// TestApplyIronProxy_AdoptInPlace_StartsGuestOriginListeners covers F4:
// adopt-in-place (shell.go's "pristine: running but never provisioned"
// branch) calls /vm/apply-iron-proxy directly and never /vm/start, so
// nothing has ever started this project's guest-origin listener pair —
// in-guest `.test` would hairpin to softnet's gateway address with
// nothing on the other end. With a real *ProxyServer wired in (mirrors
// runner.go's production wiring), the handler must start the pair
// itself and record the ports in the registry.
func TestApplyIronProxy_AdoptInPlace_StartsGuestOriginListeners(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := NewServer(identity.Prod.SocketPath(), Build{})
	sup := supervisor.New(t.TempDir())

	const projectID = "p-adopt-guest-listeners"
	t.Cleanup(func() {
		ironProxyState.del(projectID)
		ReleaseProjectIP(identity.Prod, projectID)
	})

	seededCfg := schema.Config{Project: schema.Project{Name: projectID}}
	require.NoError(t, WriteStateSnapshot(identity.Prod, projectID, StateSnapshot{Cfg: seededCfg}))

	macHost := "127.0.0.1"
	httpPort, err := pickPort()
	require.NoError(t, err)
	httpsPort, err := pickPort()
	require.NoError(t, err)
	dnsPort, err := pickPort()
	require.NoError(t, err)
	writePreExistingIronProxyConfig(t, projectID, macHost, httpPort, httpsPort, dnsPort)

	origSpawn := spawnIronProxyFn
	t.Cleanup(func() { spawnIronProxyFn = origSpawn })
	var ln net.Listener
	spawnIronProxyFn = func(_ context.Context, _ identity.Config, _ *supervisor.Supervisor, _ string, proxyCfg IronProxyConfig, _ *Denials) error {
		var lerr error
		ln, lerr = net.Listen("tcp", proxyCfg.HTTPSListen)
		return lerr
	}

	ca, err := loadOrGenerateCAAt(identity.Prod, t.TempDir())
	require.NoError(t, err)
	proxy := NewProxyServer(identity.Prod, NewRoutes(), ca)
	t.Cleanup(proxy.StopAll)

	RegisterApplyIronProxyHandler(srv, identity.Prod, NewProjectLocks(), sup, fakeTartIPFails(), nil, proxy)

	reqBody, _ := json.Marshal(VMApplyIronProxyRequest{
		Name:      projectID,
		Allowlist: []string{"a.example.com"},
	})
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/apply-iron-proxy", bytes.NewReader(reqBody)))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	if ln != nil {
		defer ln.Close()
	}

	info, ok := ironProxyState.get(projectID)
	require.True(t, ok)
	assert.NotZero(t, info.GuestHTTPPort,
		"apply-iron-proxy must start the guest-origin HTTP listener for an adopted VM and record its port")
	assert.NotZero(t, info.GuestHTTPSPort,
		"apply-iron-proxy must start the guest-origin HTTPS listener for an adopted VM and record its port")
}

// TestMergeAllowlistAndSecrets_RebuildsAllowFromApplied pins that the
// merge helper's allow-list handling: the returned Cfg's Network.Allow
// exactly reflects the applied list, with per-host secret scope
// preserved for hosts that already existed in snapCfg.
func TestMergeAllowlistAndSecrets_RebuildsAllowFromApplied(t *testing.T) {
	snapCfg := schema.Config{
		Network: schema.Network{
			Allow: []schema.AllowEntry{
				{Host: "keep.example.com", Secrets: []string{"scoped_secret"}},
				{Host: "remove.example.com"},
			},
		},
	}
	applied := []string{"keep.example.com", "new.example.com"}

	got := mergeAllowlistAndSecrets(snapCfg, applied, nil)

	require.Len(t, got.Network.Allow, 2)
	// Order matches applied.
	assert.Equal(t, "keep.example.com", got.Network.Allow[0].Host)
	assert.Equal(t, []string{"scoped_secret"}, got.Network.Allow[0].Secrets,
		"per-host secret scope must be preserved for hosts that already existed")
	assert.Equal(t, "new.example.com", got.Network.Allow[1].Host)
	assert.Nil(t, got.Network.Allow[1].Secrets, "new hosts get empty scope")
	// remove.example.com is gone.
	for _, e := range got.Network.Allow {
		assert.NotEqual(t, "remove.example.com", e.Host)
	}
}

// TestMergeAllowlistAndSecrets_ClearsUnboundSecretRefs pins that env
// values whose secret name is not in the applied secret bindings are
// dropped from snap.Cfg.Env and snap.Cfg.Services[*].Env. Literal env
// values are untouched.
func TestMergeAllowlistAndSecrets_ClearsUnboundSecretRefs(t *testing.T) {
	snapCfg := schema.Config{
		Env: map[string]schema.EnvValue{
			"KEEP_LITERAL":  {Literal: "hello"},
			"KEEP_SECRET":   {Secret: &schema.SecretRef{Name: "bound_secret"}},
			"REMOVE_SECRET": {Secret: &schema.SecretRef{Name: "unbound_secret"}},
		},
		Services: map[string]schema.Service{
			"svc": {
				Env: map[string]schema.EnvValue{
					"SVC_KEEP":   {Literal: "world"},
					"SVC_SECRET": {Secret: &schema.SecretRef{Name: "unbound_secret"}},
				},
			},
		},
	}
	appliedSecrets := []SecretBinding{
		{Name: "bound_secret", Value: "s3cr3t"},
	}

	got := mergeAllowlistAndSecrets(snapCfg, nil, appliedSecrets)

	// Global Env: literal preserved, bound secret ref preserved,
	// unbound secret ref dropped.
	assert.Equal(t, "hello", got.Env["KEEP_LITERAL"].Literal)
	require.NotNil(t, got.Env["KEEP_SECRET"].Secret)
	assert.Equal(t, "bound_secret", got.Env["KEEP_SECRET"].Secret.Name)
	_, hasRemoved := got.Env["REMOVE_SECRET"]
	assert.False(t, hasRemoved, "REMOVE_SECRET (unbound) must be dropped")

	// Per-service Env: same treatment.
	svc := got.Services["svc"]
	assert.Equal(t, "world", svc.Env["SVC_KEEP"].Literal)
	_, hasSvcSecret := svc.Env["SVC_SECRET"]
	assert.False(t, hasSvcSecret, "SVC_SECRET (unbound) must be dropped")
}
