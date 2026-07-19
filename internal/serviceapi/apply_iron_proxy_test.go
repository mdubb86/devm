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
	path, err := IronProxyConfigPath(projectID)
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
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())
	srv := NewServer(SocketPath(), Build{})
	sup := supervisor.New("")
	RegisterApplyIronProxyHandler(srv, NewProjectLocks(), sup, fakeTartIPFails(), nil)

	// Simulate cold-start (`devm start` / `devm shell`) having already
	// seeded the snapshot with the real schema.Config — a prior
	// /vm/start ran for this project, but no iron-proxy config file has
	// been written yet (e.g. VM was stopped again before ever spawning
	// iron-proxy).
	seededCfg := schema.Config{Project: schema.Project{Name: "p"}}
	require.NoError(t, WriteStateSnapshot("p", StateSnapshot{Cfg: seededCfg}))

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
	// schema without re-detecting the same drift. The seeded Cfg must
	// be preserved, not clobbered with a zero value.
	snap, err := ReadStateSnapshot("p")
	require.NoError(t, err)
	require.NotNil(t, snap)
	assert.Equal(t, seededCfg, snap.Cfg, "cfg must be preserved, not zeroed")
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
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())
	srv := NewServer(SocketPath(), Build{})
	sup := supervisor.New("")
	RegisterApplyIronProxyHandler(srv, NewProjectLocks(), sup, fakeTartIPFails(), nil)

	body, _ := json.Marshal(VMApplyIronProxyRequest{
		Name:      "never-started",
		Allowlist: []string{"a.example.com"},
	})
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/apply-iron-proxy", bytes.NewReader(body)))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	snap, err := ReadStateSnapshot("never-started")
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
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())
	srv := NewServer(SocketPath(), Build{})
	sup := supervisor.New(t.TempDir())

	const projectID = "p-running"
	// Simulate cold-start having already seeded the snapshot with the
	// real schema.Config; apply-iron-proxy requires this to exist (F3).
	seededCfg := schema.Config{Project: schema.Project{Name: projectID}}
	require.NoError(t, WriteStateSnapshot(projectID, StateSnapshot{Cfg: seededCfg}))

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
	spawnIronProxyFn = func(_ context.Context, _ *supervisor.Supervisor, _ string, cfg IronProxyConfig, _ *Denials) error {
		var lerr error
		ln, lerr = net.Listen("tcp", cfg.HTTPSListen)
		return lerr
	}

	t.Cleanup(func() { ironProxyState.del(projectID) })
	RegisterApplyIronProxyHandler(srv, NewProjectLocks(), sup, fakeTartIP(t, "192.168.64.50"), nil)

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

	snap, err := ReadStateSnapshot(projectID)
	require.NoError(t, err)
	require.NotNil(t, snap)
	require.Contains(t, snap.SecretHashes, "github_token")
	assert.Equal(t, seededCfg, snap.Cfg, "cfg must be preserved, not zeroed")

	_, ok := ironProxyState.get(projectID)
	require.True(t, ok, "ironProxyState must hold an entry for the project after a successful apply")
}

// TestApplyIronProxy_PreservesSSHHostPort covers a reconcile-driven
// BucketEgressRestart apply (allowlist/secret drift) against a VM that's
// still running this daemon lifetime: ironProxyState already holds the
// SSH host port /vm/start allocated. The handler rebuilds `info` from
// iron-proxy's on-disk YAML config (loadIronProxyInfoFromConfig), which
// has no notion of SSH — without carrying SSHHostPort forward from the
// pre-existing entry, this call would silently zero it out, and the
// next live reconcile's expose-map push would drop the guest's SSH
// listener.
func TestApplyIronProxy_PreservesSSHHostPort(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())
	srv := NewServer(SocketPath(), Build{})
	sup := supervisor.New(t.TempDir())

	const projectID = "p-preserve-ssh"
	t.Cleanup(func() { ironProxyState.del(projectID) })

	seededCfg := schema.Config{Project: schema.Project{Name: projectID}}
	require.NoError(t, WriteStateSnapshot(projectID, StateSnapshot{Cfg: seededCfg, SSHHostPort: 2200}))

	macHost := "127.0.0.1"
	httpPort, err := pickPort()
	require.NoError(t, err)
	httpsPort, err := pickPort()
	require.NoError(t, err)
	dnsPort, err := pickPort()
	require.NoError(t, err)
	writePreExistingIronProxyConfig(t, projectID, macHost, httpPort, httpsPort, dnsPort)

	// The VM is still running this daemon lifetime: ironProxyState
	// already holds the SSH host port /vm/start allocated.
	ironProxyState.put(projectID, projectInfo{SSHHostPort: 2200})

	origSpawn := spawnIronProxyFn
	t.Cleanup(func() { spawnIronProxyFn = origSpawn })
	var ln net.Listener
	spawnIronProxyFn = func(_ context.Context, _ *supervisor.Supervisor, _ string, cfg IronProxyConfig, _ *Denials) error {
		var lerr error
		ln, lerr = net.Listen("tcp", cfg.HTTPSListen)
		return lerr
	}

	RegisterApplyIronProxyHandler(srv, NewProjectLocks(), sup, fakeTartIPFails(), nil)

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
	assert.Equal(t, 2200, info.SSHHostPort,
		"SSHHostPort must be preserved across apply-iron-proxy, not zeroed")
}

// TestApplyIronProxy_AllocatesSSHHostPortWhenUnset covers adopt-in-place
// (internal/orchestrator/shell.go's "pristine: running but never
// provisioned" branch): a raw `tart run` or first-time adoption calls
// /vm/apply-iron-proxy directly, never /vm/start, so no SSH host port
// was ever allocated for this project this daemon lifetime. Without
// allocating one here, the adopted VM gets no ingress — SSH port stays
// 0, no service listeners — until an explicit stop + cold-start.
func TestApplyIronProxy_AllocatesSSHHostPortWhenUnset(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())
	srv := NewServer(SocketPath(), Build{})
	sup := supervisor.New(t.TempDir())

	const projectID = "p-adopt-in-place"
	t.Cleanup(func() { ironProxyState.del(projectID) })

	seededCfg := schema.Config{
		Project: schema.Project{Name: projectID},
		Services: map[string]schema.Service{
			"db": {Port: 5432},
		},
	}
	require.NoError(t, WriteStateSnapshot(projectID, StateSnapshot{Cfg: seededCfg, SSHHostPort: 0}))

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
	spawnIronProxyFn = func(_ context.Context, _ *supervisor.Supervisor, _ string, cfg IronProxyConfig, _ *Denials) error {
		var lerr error
		ln, lerr = net.Listen("tcp", cfg.HTTPSListen)
		return lerr
	}

	RegisterApplyIronProxyHandler(srv, NewProjectLocks(), sup, fakeTartIPFails(), nil)

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
	assert.NotZero(t, info.SSHHostPort,
		"apply-iron-proxy must allocate an SSH host port for an adopted VM that never went through /vm/start")
}
