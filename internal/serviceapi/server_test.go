package serviceapi

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestServer returns a Server bound to a temp socket plus a cleanup func.
// Uses /tmp directly to stay within macOS's 104-byte sun_path limit.
func newTestServer(t *testing.T) (*Server, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sapi-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s.sock")
	srv := NewServer(socket, Build{Version: "test-version"})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()

	// Poll for the socket to appear.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.FileExists(t, socket)

	return srv, func() {
		cancel()
		<-errCh
	}
}

func dialSocket(t *testing.T, socket string) *http.Client {
	t.Helper()
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		},
		Timeout: 2 * time.Second,
	}
}

func TestServer_HealthReturns200(t *testing.T) {
	srv, cleanup := newTestServer(t)
	defer cleanup()

	c := dialSocket(t, srv.socketPath)
	resp, err := c.Get("http://localhost/health")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
}

func TestServer_VersionReturnsBuildVersion(t *testing.T) {
	srv, cleanup := newTestServer(t)
	defer cleanup()

	c := dialSocket(t, srv.socketPath)
	resp, err := c.Get("http://localhost/version")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, string(body), "test-version")
}

func TestServer_SocketIs0600(t *testing.T) {
	srv, cleanup := newTestServer(t)
	defer cleanup()

	info, err := os.Stat(srv.socketPath)
	require.NoError(t, err)
	// On macOS the perms after chmod are 0600.
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

func TestServer_SetProxyReady_UpdatesCache(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "s.sock")
	srv := NewServer(socket, Build{Version: "test-version"})
	cache := NewStateCache()
	srv.SetStateCache(cache)

	srv.SetProxyReady(true)
	assert.True(t, cache.Global().ProxyReady, "cache must reflect the proxy-ready flag")

	srv.SetProxyReady(false)
	assert.False(t, cache.Global().ProxyReady, "cache must reflect a later flip back to not-ready")
}

func TestServer_HandleProxyStatus_ServesFromCache(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "s.sock")
	srv := NewServer(socket, Build{Version: "test-version"})
	cache := NewStateCache()
	cache.SetProxyReady(true)
	srv.SetStateCache(cache)

	req := httptest.NewRequest("GET", "/proxy-status", nil)
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"ready":true}`, rec.Body.String(),
		"handleProxyStatus must read from cache when SetStateCache has run")

	// Flip in the cache; response tracks it.
	cache.SetProxyReady(false)
	rec = httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	assert.JSONEq(t, `{"ready":false}`, rec.Body.String())
}

func TestServer_HandleProxyStatus_FallsBackToAtomic_WhenCacheUnset(t *testing.T) {
	// The nil-cache fallback runs when SetStateCache hasn't been called
	// yet (bootstrap ordering safety). In production, runner.go always
	// wires the cache before Serve accepts requests, but the branch has
	// to be there for the defensive guarantee.
	dir := t.TempDir()
	socket := filepath.Join(dir, "s.sock")
	srv := NewServer(socket, Build{Version: "test-version"})
	// Deliberately do NOT SetStateCache.
	srv.proxyReady.Store(true)

	req := httptest.NewRequest("GET", "/proxy-status", nil)
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"ready":true}`, rec.Body.String(),
		"nil-cache path must fall back to the atomic flag")
}

func TestServer_RegisterAddsEndpoint(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "s.sock")
	srv := NewServer(socket, Build{Version: "test-version"})

	srv.Register("/custom", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		w.Write([]byte("hello"))
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	c := dialSocket(t, socket)
	resp, err := c.Get("http://localhost/custom")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 201, resp.StatusCode)
	assert.Equal(t, "hello", string(body))
}
