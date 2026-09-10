package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shortTempDir returns a fresh temp directory with a short, fixed-length
// name. t.TempDir() nests under a path derived from the test name, which
// overruns a Unix socket's ~104-byte sun_path limit for longer test names.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "devm-testproxy-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// newFakeDaemon starts an HTTP server on a Unix socket standing in
// for the real devm daemon, returning the socket path.
func newFakeDaemon(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	sockPath := filepath.Join(shortTempDir(t), "d.sock")
	ln, err := net.Listen("unix", sockPath)
	require.NoError(t, err)

	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	return sockPath
}

func TestProxy_ForwardsRequestToUnixSocket(t *testing.T) {
	sockPath := newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"received":"`+r.URL.Path+`"}`)
	})

	ts := httptest.NewServer(newProxy(sockPath))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/vm/status/all")
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, `{"received":"/vm/status/all"}`, string(body))
}

func TestProxy_SetsCORSHeaderOnResponse(t *testing.T) {
	sockPath := newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})

	ts := httptest.NewServer(newProxy(sockPath))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/version")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
}

func TestProxy_QueryStringPreserved(t *testing.T) {
	sockPath := newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.URL.RawQuery)
	})

	ts := httptest.NewServer(newProxy(sockPath))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/vm/logs?name=foo&tail=50")
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "name=foo&tail=50", string(body))
}

func TestProxy_StreamsChunkedResponseBody(t *testing.T) {
	sockPath := newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		require.True(t, ok, "ResponseWriter must support flushing for this test to prove streaming")
		for i := 0; i < 3; i++ {
			_, _ = io.WriteString(w, "chunk")
			flusher.Flush()
		}
	})

	ts := httptest.NewServer(newProxy(sockPath))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/stream")
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "chunkchunkchunk", string(body))
}

func TestProxy_UnixSocketMissing_Returns502(t *testing.T) {
	missing := filepath.Join(shortTempDir(t), "missing.sock")

	ts := httptest.NewServer(newProxy(missing))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/vm/status/all")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
}

func TestRun_ListensOnTCPAndForwards(t *testing.T) {
	sockPath := newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok:"+r.URL.Path)
	})

	// Reserve a free TCP port, then hand it to run(). run() only stops
	// on an OS signal to the whole process, so this goroutine outlives
	// the test — acceptable for a short-lived test binary.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := probe.Addr().(*net.TCPAddr).Port
	require.NoError(t, probe.Close())

	go func() { _ = run(port, sockPath) }()

	addr := "127.0.0.1:" + strconv.Itoa(port)
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, 2*time.Second, 10*time.Millisecond, "devm-testproxy never started listening on %s", addr)

	resp, err := http.Get("http://" + addr + "/vm/status/all")
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "ok:/vm/status/all", string(body))
}
