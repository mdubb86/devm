package serviceapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mdubb86/devm/internal/identity"
)

// startBackend boots a tiny HTTP server on a random port that
// returns `msg` for every GET. Used by the proxy tests to verify
// end-to-end traffic.
func startBackend(t *testing.T, msg string) (port int, cleanup func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(msg))
	}))
	// httptest URL shape: http://127.0.0.1:PORT
	url := strings.TrimPrefix(srv.URL, "http://")
	_, portStr, err := net.SplitHostPort(url)
	require.NoError(t, err)
	p, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	return p, srv.Close
}

// withLocalAddr stamps the given IP into the request context exactly
// as ProxyServer.stampLocalAddr does via ConnContext on a real
// accepted connection — lets tests exercise the dest-IP dispatch path
// without binding real per-project listeners (which requires the
// helper).
func withLocalAddr(r *http.Request, ip string) *http.Request {
	ctx := context.WithValue(r.Context(), ctxKeyLocalAddr, &net.TCPAddr{IP: net.ParseIP(ip)})
	return r.WithContext(ctx)
}

// registerProject stashes a project's ProjectIP in ironProxyState (the
// package-global registry ServeHTTP's projectByIP reads) and returns a
// cleanup func. Tests must call it so package-global state doesn't
// leak across tests.
func registerProject(t *testing.T, projectID, ip string) {
	t.Helper()
	ironProxyState.put(projectID, projectInfo{ProjectIP: ip})
	t.Cleanup(func() { ironProxyState.del(projectID) })
}

func TestProxy_HTTP_RoutesByHostWithinProject(t *testing.T) {
	backPort, cleanup := startBackend(t, "hello from backend")
	defer cleanup()

	routes := NewRoutes()
	routes.Apply("p1", []Route{
		{Hostname: "app.test", BackendPort: backPort, Mode: ModeLocal, Project: "p1"},
	})
	dir := t.TempDir()
	ca, err := loadOrGenerateCAAt(identity.Prod, dir)
	require.NoError(t, err)

	registerProject(t, "p1", "127.42.0.50")
	proxy := NewProxyServer(identity.Prod, routes, ca)

	req := httptest.NewRequest(http.MethodGet, "http://app.test/", nil)
	req.Host = "app.test"
	req = withLocalAddr(req, "127.42.0.50")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	resp := rec.Result()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "hello from backend", string(body))
}

func TestProxy_NoLocalAddrInContext_502(t *testing.T) {
	routes := NewRoutes()
	dir := t.TempDir()
	ca, err := loadOrGenerateCAAt(identity.Prod, dir)
	require.NoError(t, err)
	proxy := NewProxyServer(identity.Prod, routes, ca)

	req := httptest.NewRequest(http.MethodGet, "http://unknown.test/", nil)
	req.Host = "unknown.test"
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	resp := rec.Result()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 502, resp.StatusCode)
	assert.Contains(t, string(body), "no route configured for unknown.test")
}

func TestProxy_DestIPWithNoProject_502NoProject(t *testing.T) {
	routes := NewRoutes()
	dir := t.TempDir()
	ca, err := loadOrGenerateCAAt(identity.Prod, dir)
	require.NoError(t, err)
	proxy := NewProxyServer(identity.Prod, routes, ca)

	req := httptest.NewRequest(http.MethodGet, "http://app.test/", nil)
	req.Host = "app.test"
	req = withLocalAddr(req, "127.42.0.99") // no project owns this IP
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	resp := rec.Result()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 502, resp.StatusCode)
	assert.Contains(t, string(body), "no project bound at 127.42.0.99")
}

// TestProxy_HostMismatchAcrossProjects_502NotFallthrough pins the
// isolation guarantee from the design doc: a Host header naming a
// hostname that belongs to a DIFFERENT project than the one that owns
// the dest IP must 502, never silently dial the other project's
// backend.
func TestProxy_HostMismatchAcrossProjects_502NotFallthrough(t *testing.T) {
	backPort, cleanup := startBackend(t, "p1's backend")
	defer cleanup()

	routes := NewRoutes()
	routes.Apply("p1", []Route{
		{Hostname: "app.test", BackendPort: backPort, Mode: ModeLocal, Project: "p1"},
	})
	dir := t.TempDir()
	ca, err := loadOrGenerateCAAt(identity.Prod, dir)
	require.NoError(t, err)

	registerProject(t, "p1", "127.42.0.50")
	registerProject(t, "p2", "127.42.0.51")
	proxy := NewProxyServer(identity.Prod, routes, ca)

	// Dial p2's IP but ask for p1's hostname.
	req := httptest.NewRequest(http.MethodGet, "http://app.test/", nil)
	req.Host = "app.test"
	req = withLocalAddr(req, "127.42.0.51")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	resp := rec.Result()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 502, resp.StatusCode)
	assert.Contains(t, string(body), "no route configured for app.test")
	assert.NotContains(t, string(body), "p1's backend")
}

func TestProxy_BackendUnreachable_502WithDiagnostic(t *testing.T) {
	routes := NewRoutes()
	routes.Apply("p1", []Route{
		// Port unlikely to be in use — high in the dynamic range.
		{Hostname: "down.test", BackendPort: 59999, Mode: ModeVM, Project: "p1"},
	})
	dir := t.TempDir()
	ca, _ := loadOrGenerateCAAt(identity.Prod, dir)
	registerProject(t, "p1", "127.42.0.52")
	proxy := NewProxyServer(identity.Prod, routes, ca)

	req := httptest.NewRequest(http.MethodGet, "http://down.test/", nil)
	req.Host = "down.test"
	req = withLocalAddr(req, "127.42.0.52")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	resp := rec.Result()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 502, resp.StatusCode)
	assert.Contains(t, string(body), "no service listening at down.test")
}

func TestProxy_BackendHost_ExplicitLocalhost(t *testing.T) {
	backPort, cleanup := startBackend(t, "from backend")
	defer cleanup()

	routes := NewRoutes()
	routes.Apply("p1", []Route{
		{Hostname: "app.test", BackendHost: "127.0.0.1", BackendPort: backPort, Mode: ModeLocal, Project: "p1"},
	})
	dir := t.TempDir()
	ca, err := loadOrGenerateCAAt(identity.Prod, dir)
	require.NoError(t, err)

	registerProject(t, "p1", "127.42.0.53")
	proxy := NewProxyServer(identity.Prod, routes, ca)

	req := httptest.NewRequest(http.MethodGet, "http://app.test/", nil)
	req.Host = "app.test"
	req = withLocalAddr(req, "127.42.0.53")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	resp := rec.Result()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "from backend", string(body))
}

// mockHelperServer starts a UDS listener that mimics enough of the
// root devm-helper's protocol to satisfy StartProjectListeners: for
// every connection it reads (and discards) one newline-delimited
// request, binds a real ephemeral TCP socket, and hands the FD back
// via SCM_RIGHTS. Serves connections in a loop — StartProjectListeners
// dials it twice (once for :80, once for :443).
func mockHelperServer(t *testing.T) string {
	t.Helper()
	// os.MkdirTemp (not t.TempDir()) keeps the UDS path short enough to
	// stay under macOS's ~104-byte UNIX_PATH_MAX; t.TempDir() embeds
	// the test name and can overflow it.
	dir, err := os.MkdirTemp("", "pxy")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "helper.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				uc := c.(*net.UnixConn)
				if _, err := bufio.NewReader(uc).ReadBytes('\n'); err != nil {
					return
				}
				fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
				if err != nil {
					return
				}
				defer syscall.Close(fd)
				addr := &syscall.SockaddrInet4{Port: 0}
				copy(addr.Addr[:], []byte{127, 0, 0, 1})
				if err := syscall.Bind(fd, addr); err != nil {
					return
				}
				if err := syscall.Listen(fd, 8); err != nil {
					return
				}
				resp, _ := json.Marshal(struct {
					OK bool `json:"ok"`
				}{OK: true})
				oob := syscall.UnixRights(fd)
				_, _, _ = uc.WriteMsgUnix(resp, oob, nil)
			}(conn)
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		os.Remove(sock)
	})
	return sock
}

// TestProxyServer_DialsCfgHelperSocket_NotProdHardcoded is the C1
// regression test: before the fix, helper.SocketPath was a
// package-level var hardcoded to "/var/run/devm-helper.sock" and
// StartProjectListeners always dialed it regardless of the daemon's
// identity — an e2e daemon (or any non-Prod cfg) would dial prod's
// helper socket (or fail outright, since prod's helper isn't installed
// in a test/e2e sandbox). Here cfg.HelperSocketPath points at a scratch
// UDS with an obviously non-prod Name; StartProjectListeners only
// succeeds if ProxyServer actually threads cfg through to the helper
// client instead of falling back to the old hardcoded path (which
// doesn't exist in this test's sandbox).
func TestProxyServer_DialsCfgHelperSocket_NotProdHardcoded(t *testing.T) {
	sock := mockHelperServer(t)
	cfg := identity.Config{Name: "test-proxy-not-prod", HelperSocketPath: sock}

	dir := t.TempDir()
	ca, err := loadOrGenerateCAAt(identity.Prod, dir)
	require.NoError(t, err)

	proxy := NewProxyServer(cfg, NewRoutes(), ca)
	err = proxy.StartProjectListeners(context.Background(), "p1", "127.0.0.1")
	require.NoError(t, err, "StartProjectListeners must dial cfg.HelperSocketPath, not a hardcoded prod path")
	t.Cleanup(func() { proxy.StopProjectListeners("p1") })
}
