package main

import (
	"context"
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

	cfg := schema.Config{Project: schema.Project{ID: "p", VMName: "p-vm"}}
	err := daemonHandshake(context.Background(), cfg)
	assert.NoError(t, err)
}

func TestDaemonHandshake_FingerprintDrift_ReturnsActionableError(t *testing.T) {
	origFingerprint := Fingerprint
	Fingerprint = "fp-cli"
	t.Cleanup(func() { Fingerprint = origFingerprint })

	cleanup := startHandshakeDaemon(t, serviceapi.Build{Fingerprint: "fp-daemon", BinaryPath: "/daemon/path"})
	defer cleanup()

	cfg := schema.Config{Project: schema.Project{ID: "p", VMName: "p-vm"}}
	err := daemonHandshake(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "devm daemon is out of sync with this CLI")
	assert.Contains(t, err.Error(), "fp-daemon")
	assert.Contains(t, err.Error(), "fp-cli")
	assert.Contains(t, err.Error(), "devm install")
}

func TestDaemonHandshake_DaemonUnreachable_ToleratedNoError(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir()) // no daemon listening here
	cfg := schema.Config{Project: schema.Project{ID: "p", VMName: "p-vm"}}
	err := daemonHandshake(context.Background(), cfg)
	assert.NoError(t, err)
}
