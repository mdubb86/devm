package serviceapi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestProxy_HTTP_RoutesByHost(t *testing.T) {
	backPort, cleanup := startBackend(t, "hello from backend")
	defer cleanup()

	routes := NewRoutes()
	routes.Apply("p1", []Route{
		{Hostname: "app.test", BackendPort: backPort, Mode: ModeLocal},
	})
	dir := t.TempDir()
	ca, err := loadOrGenerateCAAt(dir)
	require.NoError(t, err)

	proxy := NewProxyServer(routes, ca)
	httpLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = proxy.Serve(ctx, []net.Listener{httpLn}, nil) }()
	time.Sleep(100 * time.Millisecond)

	req, err := http.NewRequest("GET", "http://"+httpLn.Addr().String()+"/", nil)
	require.NoError(t, err)
	req.Host = "app.test"
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "hello from backend", string(body))
}

func TestProxy_NoRoute_502(t *testing.T) {
	routes := NewRoutes()
	dir := t.TempDir()
	ca, err := loadOrGenerateCAAt(dir)
	require.NoError(t, err)

	proxy := NewProxyServer(routes, ca)
	httpLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = proxy.Serve(ctx, []net.Listener{httpLn}, nil) }()
	time.Sleep(100 * time.Millisecond)

	req, _ := http.NewRequest("GET", "http://"+httpLn.Addr().String()+"/", nil)
	req.Host = "unknown.test"
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 502, resp.StatusCode)
	assert.Contains(t, string(body), "no route configured for unknown.test")
}

func TestProxy_BackendUnreachable_502WithDiagnostic(t *testing.T) {
	routes := NewRoutes()
	routes.Apply("p1", []Route{
		// Port unlikely to be in use — high in the dynamic range.
		{Hostname: "down.test", BackendPort: 59999, Mode: ModeVM},
	})
	dir := t.TempDir()
	ca, _ := loadOrGenerateCAAt(dir)
	proxy := NewProxyServer(routes, ca)
	httpLn, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = proxy.Serve(ctx, []net.Listener{httpLn}, nil) }()
	time.Sleep(100 * time.Millisecond)

	req, _ := http.NewRequest("GET", "http://"+httpLn.Addr().String()+"/", nil)
	req.Host = "down.test"
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 502, resp.StatusCode)
	assert.Contains(t, string(body), "no service listening at down.test")
}

func TestProxy_BackendHost_ExplicitLocalhost(t *testing.T) {
	backPort, cleanup := startBackend(t, "from backend")
	defer cleanup()

	routes := NewRoutes()
	routes.Apply("p1", []Route{
		{Hostname: "app.test", BackendHost: "127.0.0.1", BackendPort: backPort, Mode: ModeLocal},
	})
	dir := t.TempDir()
	ca, err := loadOrGenerateCAAt(dir)
	require.NoError(t, err)

	proxy := NewProxyServer(routes, ca)
	httpLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = proxy.Serve(ctx, []net.Listener{httpLn}, nil) }()
	time.Sleep(100 * time.Millisecond)

	req, err := http.NewRequest("GET", "http://"+httpLn.Addr().String()+"/", nil)
	require.NoError(t, err)
	req.Host = "app.test"
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "from backend", string(body))
}

func TestProxy_HTTPS_ServesCertViaCA(t *testing.T) {
	backPort, cleanup := startBackend(t, "https-backend")
	defer cleanup()

	routes := NewRoutes()
	routes.Apply("p1", []Route{
		{Hostname: "secure.test", BackendPort: backPort, Mode: ModeLocal},
	})
	dir := t.TempDir()
	ca, err := loadOrGenerateCAAt(dir)
	require.NoError(t, err)

	proxy := NewProxyServer(routes, ca)
	httpsLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = proxy.Serve(ctx, nil, []net.Listener{httpsLn}) }()
	time.Sleep(100 * time.Millisecond)

	// Build a TLS client that trusts our root.
	rootCert, _ := x509.ParseCertificate(ca.rootCert.Raw)
	roots := x509.NewCertPool()
	roots.AddCert(rootCert)
	clientCfg := &tls.Config{RootCAs: roots, ServerName: "secure.test"}

	conn, err := tls.Dial("tcp", httpsLn.Addr().String(), clientCfg)
	require.NoError(t, err)
	defer conn.Close()

	_, err = conn.Write([]byte("GET / HTTP/1.1\r\nHost: secure.test\r\nConnection: close\r\n\r\n"))
	require.NoError(t, err)
	all, _ := io.ReadAll(conn)
	assert.Contains(t, string(all), "https-backend")
}
