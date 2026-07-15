package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/mdubb86/devm/internal/supervisor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureStderr redirects os.Stderr for the duration of fn and returns
// everything written to it. Used to assert daemonHandshake's drift
// warning without needing a real terminal.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = orig })

	fn()

	require.NoError(t, w.Close())
	os.Stderr = orig
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out)
}

// startHandshakeDaemon spins a real serviceapi.Server with only the
// /handshake endpoint registered, bound to a temp socket under
// $DEVM_RUNTIME_DIR. daemonHandshake talks to it via the default
// serviceapi.NewClient(), which resolves the socket from that env var.
func startHandshakeDaemon(t *testing.T, build serviceapi.Build) func() {
	t.Helper()
	// os.MkdirTemp("/tmp", ...) rather than t.TempDir(): the latter nests
	// under a path keyed on the test name, which blows macOS's ~104-byte
	// AF_UNIX path limit for tests with long names.
	dir, err := os.MkdirTemp("/tmp", "sapi-hs-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("DEVM_RUNTIME_DIR", dir)

	srv := serviceapi.NewServer(serviceapi.SocketPath(), build)
	sup := supervisor.New("")
	serviceapi.RegisterHandshakeHandler(srv, build, sup)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "devm.sock")); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.FileExists(t, filepath.Join(dir, "devm.sock"))

	return func() { cancel(); <-errCh }
}

func TestDaemonHandshake_FingerprintMatch_NoError(t *testing.T) {
	origFingerprint := Fingerprint
	Fingerprint = "fp-match"
	t.Cleanup(func() { Fingerprint = origFingerprint })

	cleanup := startHandshakeDaemon(t, serviceapi.Build{Fingerprint: "fp-match"})
	defer cleanup()

	cfg := schema.Config{Project: schema.Project{Name: "p"}}
	err := daemonHandshake(context.Background(), cfg)
	assert.NoError(t, err)
}

func TestDaemonHandshake_FingerprintDrift_ReturnsActionableError(t *testing.T) {
	origFingerprint := Fingerprint
	Fingerprint = "fp-cli"
	t.Cleanup(func() { Fingerprint = origFingerprint })

	cleanup := startHandshakeDaemon(t, serviceapi.Build{Fingerprint: "fp-daemon", BinaryPath: "/daemon/path"})
	defer cleanup()

	cfg := schema.Config{Project: schema.Project{Name: "p"}}
	err := daemonHandshake(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "devm daemon is out of sync with this CLI")
	assert.Contains(t, err.Error(), "fp-daemon")
	assert.Contains(t, err.Error(), "fp-cli")
	assert.Contains(t, err.Error(), "devm install")
}

func TestDaemonHandshake_DaemonUnreachable_ToleratedNoError(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir()) // no daemon listening here
	cfg := schema.Config{Project: schema.Project{Name: "p"}}
	err := daemonHandshake(context.Background(), cfg)
	assert.NoError(t, err)
}

// TestDaemonHandshake_ProxyDrift_WarnsAndDoesNotHeal covers the
// collapsed heal model: `devm reconcile` is the only thing that heals
// iron-proxy drift now. daemonHandshake must not attempt to heal it —
// only report it via a stderr warning — even though the daemon in this
// test has no /vm/apply-iron-proxy handler registered at all (so any
// attempt to heal would itself fail loudly, which is exactly what we're
// asserting does NOT happen: no error, just a warning).
func TestDaemonHandshake_ProxyDrift_WarnsAndDoesNotHeal(t *testing.T) {
	origFingerprint := Fingerprint
	Fingerprint = "fp-match"
	t.Cleanup(func() { Fingerprint = origFingerprint })

	// A fresh supervisor + no state snapshot for "p" means
	// computeProxyHealth reports ProxyMissing (no live process, no
	// config file on disk).
	cleanup := startHandshakeDaemon(t, serviceapi.Build{Fingerprint: "fp-match"})
	defer cleanup()

	cfg := schema.Config{Project: schema.Project{Name: "p"}}

	var err error
	stderr := captureStderr(t, func() {
		err = daemonHandshake(context.Background(), cfg)
	})

	assert.NoError(t, err, "drift must be reported, not surfaced as an error")
	assert.Contains(t, stderr, "warning: iron-proxy for p is missing")
	assert.Contains(t, stderr, "devm reconcile")
}

// TestDaemonHandshake_ProxyDrift_VMStopped_NoWarning pins the "don't
// nag when a cold-start will heal" case: post-upgrade or after any
// stop, computeProxyHealth reports ProxyMissing, but `devm shell` /
// `devm start` will cold-start and /vm/start respawns iron-proxy
// fresh. Warning users to run `devm reconcile` in that state tells
// them to run a command that's redundant with what they just typed.
//
// The test daemon here also serves a stub /vm/status returning
// Running=false, which is what vmIsRunning reads to suppress the
// warning. Contrast with the previous test which uses a daemon that
// has no /vm/status handler — vmIsRunning errors and defaults to true
// so the warning still fires (matches production behavior when the
// endpoint disappears).
func TestDaemonHandshake_ProxyDrift_VMStopped_NoWarning(t *testing.T) {
	origFingerprint := Fingerprint
	Fingerprint = "fp-match"
	t.Cleanup(func() { Fingerprint = origFingerprint })

	dir, err := os.MkdirTemp("/tmp", "sapi-hs-vmstop-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("DEVM_RUNTIME_DIR", dir)

	build := serviceapi.Build{Fingerprint: "fp-match"}
	srv := serviceapi.NewServer(serviceapi.SocketPath(), build)
	sup := supervisor.New("")
	serviceapi.RegisterHandshakeHandler(srv, build, sup)
	// Stub /vm/status returning "not running". No supervisor / tart
	// wiring needed since daemonHandshake only reads the Running field.
	srv.Register("/vm/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"present": false, "running": false}`))
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-errCh })

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "devm.sock")); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.FileExists(t, filepath.Join(dir, "devm.sock"))

	cfg := schema.Config{Project: schema.Project{Name: "p"}}

	var hsErr error
	stderr := captureStderr(t, func() {
		hsErr = daemonHandshake(context.Background(), cfg)
	})

	assert.NoError(t, hsErr)
	assert.NotContains(t, stderr, "iron-proxy",
		"warning must be suppressed when the VM is not running — cold-start will heal")
}
