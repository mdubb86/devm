package serviceapi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
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

	"github.com/mdubb86/devm/internal/identity"
)

// startGuestBackend spins up a tiny HTTP server returning body for
// every GET, and returns its ephemeral port + a cleanup fn. The
// server binds on 127.0.0.1 (httptest's default) so a guest-origin
// listener dialing projectIP=127.0.0.1 lands here.
func startGuestBackend(t *testing.T, body string) (int, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	url := strings.TrimPrefix(srv.URL, "http://")
	_, portStr, err := net.SplitHostPort(url)
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	return port, srv.Close
}

// buildHairpinPair stands up a fresh ProxyServer + its guest-origin
// HTTP and HTTPS listeners for projectID, with one route hostname
// pointing at an in-process HTTP backend serving body. Returns the
// ProxyServer, the HTTP port, the HTTPS port, and a cleanup fn.
//
// The route is registered under hostname = "api." + projectID + "." + cfg.TLD
// so the SNI mux's own-project suffix match (`.<projectID>.<tld>`)
// matches a tls.Dial with ServerName = that hostname, and the HTTP
// mux's own-project Host-header path matches a plain HTTP GET with
// the same Host.
func buildHairpinPair(t *testing.T, cfg identity.Config, projectID, body string) (*ProxyServer, int, int, func()) {
	t.Helper()
	backendPort, backendClose := startGuestBackend(t, body)

	routes := NewRoutes(cfg.TLD)
	host := "api." + projectID + "." + cfg.TLD
	require.NoError(t, routes.Apply(projectID, []Route{
		{Hostname: host, BackendPort: backendPort, Mode: ModeVM, Project: projectID},
	}))

	caDir := t.TempDir()
	ca, err := loadOrGenerateCAAt(cfg, caDir)
	require.NoError(t, err)

	proxy := NewProxyServer(cfg, routes, ca)

	httpPort, httpsPort, err := proxy.StartGuestOriginListeners(context.Background(), projectID, "127.0.0.1")
	require.NoError(t, err)

	cleanup := func() {
		proxy.StopProjectListeners(projectID)
		backendClose()
	}
	return proxy, httpPort, httpsPort, cleanup
}

// TestPeerSuffix pins the hostname-suffix matcher under both the
// one-label prod TLD ("test") and the two-label e2e TLD ("e2e.test").
func TestPeerSuffix(t *testing.T) {
	cases := []struct {
		name string
		sni  string
		tld  string
		want string
	}{
		{"peer_bare", "sibling.test", "test", "sibling.test"},
		{"peer_one_label", "api.sibling.test", "test", "sibling.test"},
		{"peer_nested", "app.sub.sibling.test", "test", "sibling.test"},
		{"peer_e2e_bare", "sibling.e2e.test", "e2e.test", "sibling.e2e.test"},
		{"peer_e2e_label", "api.sibling.e2e.test", "e2e.test", "sibling.e2e.test"},
		{"peer_e2e_nested", "app.sub.sibling.e2e.test", "e2e.test", "sibling.e2e.test"},
		{"wrong_tld", "api.sibling.example", "test", ""},
		{"tld_only", "test", "test", ""},
		{"empty_sni", "", "test", ""},
		{"empty_tld", "api.sibling.test", "", ""},
		{"e2e_in_prod_tld", "api.sibling.e2e.test", "test", "e2e.test"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := peerSuffix(tc.sni, tc.tld)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestGuestOriginHTTPS_SNIMux_OwnDomainStillWorks pins that an SNI
// matching this project's own `.<projectID>.<tld>` suffix flows
// through the pre-existing TLS termination + guestOriginHandler path.
// A GET over TLS must return the backend's body unchanged.
func TestGuestOriginHTTPS_SNIMux_OwnDomainStillWorks(t *testing.T) {
	cfg := identity.Prod
	_, _, httpsPort, cleanup := buildHairpinPair(t, cfg, "projA", "bodyA")
	defer cleanup()

	sni := "api.projA." + cfg.TLD
	addr := fmt.Sprintf("127.0.0.1:%d", httpsPort)
	tc, err := tls.Dial("tcp", addr, &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: true,
	})
	require.NoError(t, err)
	defer tc.Close()

	req, err := http.NewRequest(http.MethodGet, "https://"+sni+"/", nil)
	require.NoError(t, err)
	req.Host = sni
	require.NoError(t, req.Write(tc))

	resp, err := http.ReadResponse(bufio.NewReader(tc), req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "bodyA", string(body))
}

// TestGuestOriginHTTPS_SNIMux_PeerSplice stands up two hairpin pairs
// A and B, declares them as peers of one another, then dials A with
// SNI = api.projB.<tld>. A's SNI mux must splice the raw TCP to B's
// listener, which then terminates TLS with its own cert and serves
// B's backend body — proving neither TLS termination happened on A
// nor cert minting leaked across the project boundary.
func TestGuestOriginHTTPS_SNIMux_PeerSplice(t *testing.T) {
	cfg := identity.Prod
	proxyA, _, httpsPortA, cleanupA := buildHairpinPair(t, cfg, "projA", "bodyA")
	defer cleanupA()
	proxyB, _, httpsPortB, cleanupB := buildHairpinPair(t, cfg, "projB", "bodyB")
	defer cleanupB()

	peerBSuffix := "projB." + cfg.TLD
	peerASuffix := "projA." + cfg.TLD
	addrB := fmt.Sprintf("127.0.0.1:%d", httpsPortB)
	addrA := fmt.Sprintf("127.0.0.1:%d", httpsPortA)

	proxyA.peerHairpinsFor = func(string) map[string]string {
		return map[string]string{peerBSuffix: addrB}
	}
	proxyB.peerHairpinsFor = func(string) map[string]string {
		return map[string]string{peerASuffix: addrA}
	}

	// Client dials A but asks for B's hostname.
	sni := "api." + peerBSuffix
	tc, err := tls.Dial("tcp", addrA, &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: true,
	})
	require.NoError(t, err)
	defer tc.Close()

	// A spliced to B; B terminated TLS with its own cert for sni.
	// The handshake succeeding at all is already evidence the splice
	// worked (A has no cert for projB.<tld>; B does).
	req, err := http.NewRequest(http.MethodGet, "https://"+sni+"/", nil)
	require.NoError(t, err)
	req.Host = sni
	require.NoError(t, req.Write(tc))

	resp, err := http.ReadResponse(bufio.NewReader(tc), req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "bodyB", string(body),
		"peer splice must deliver B's body, not A's")
}

// minimalClientHello builds a hand-crafted TLS 1.2 ClientHello whose
// only extension is server_name. Just enough on-wire bytes to drive
// the mux's SNI peek. The cipher list is empty on purpose: parseSNI
// doesn't care, and keeping lengths tight keeps the fixture small.
func minimalClientHello(t *testing.T, sni string) []byte {
	t.Helper()
	n := len(sni)
	require.LessOrEqual(t, n, 0xFFFF-5, "SNI too long for a minimal hello")
	// server_name list entry: name_type(1) + name_len(2) + name(n)
	// list: list_len(2) + entry
	// extBody: list
	// ext: ext_type(2) + ext_len(2) + extBody
	// hello: version(2) + random(32) + sid_len(1) + ciphers_len(2) +
	//        comp_len(1) + comp(0) + ext_total(2) + ext
	// record: 4 (handshake header) + hello
	extBodyLen := 2 + (1 + 2 + n) // list_len + entry
	extLen := 4 + extBodyLen      // ext_type + ext_len + body
	helloBodyLen := 2 + 32 + 1 + 2 + 1 + 2 + extLen
	recordBodyLen := 4 + helloBodyLen

	var b bytes.Buffer
	// TLS record: handshake (0x16), legacy_version 0x0301, length.
	b.WriteByte(0x16)
	b.WriteByte(0x03)
	b.WriteByte(0x01)
	b.WriteByte(byte(recordBodyLen >> 8))
	b.WriteByte(byte(recordBodyLen))
	// Handshake header: ClientHello (0x01), 24-bit body length.
	b.WriteByte(0x01)
	b.WriteByte(byte(helloBodyLen >> 16))
	b.WriteByte(byte(helloBodyLen >> 8))
	b.WriteByte(byte(helloBodyLen))
	// client_version TLS 1.2
	b.WriteByte(0x03)
	b.WriteByte(0x03)
	// 32 bytes of random (all zeroes).
	b.Write(make([]byte, 32))
	// session_id_len = 0
	b.WriteByte(0x00)
	// cipher_suites len = 0
	b.WriteByte(0x00)
	b.WriteByte(0x00)
	// compression_methods len = 0
	b.WriteByte(0x00)
	// extensions_len
	b.WriteByte(byte(extLen >> 8))
	b.WriteByte(byte(extLen))
	// server_name extension: type 0x0000, body len.
	b.WriteByte(0x00)
	b.WriteByte(0x00)
	b.WriteByte(byte(extBodyLen >> 8))
	b.WriteByte(byte(extBodyLen))
	// list_len
	b.WriteByte(byte((1 + 2 + n) >> 8))
	b.WriteByte(byte(1 + 2 + n))
	// server_name entry
	b.WriteByte(0x00) // host_name
	b.WriteByte(byte(n >> 8))
	b.WriteByte(byte(n))
	b.WriteString(sni)
	return b.Bytes()
}

// TestGuestOriginHTTPS_SNIMux_UnknownSNI_UnrecognizedNameAlert pins
// the alert format on the wire: an unknown SNI yields exactly the
// 7-byte RFC 6066 unrecognized_name warning record, then close.
func TestGuestOriginHTTPS_SNIMux_UnknownSNI_UnrecognizedNameAlert(t *testing.T) {
	cfg := identity.Prod
	_, _, httpsPort, cleanup := buildHairpinPair(t, cfg, "projA", "bodyA")
	defer cleanup()

	addr := fmt.Sprintf("127.0.0.1:%d", httpsPort)
	raw, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer raw.Close()

	// Craft a hand-rolled ClientHello with an SNI the server does not
	// own and did not declare as a peer. The server must reply with
	// the warn-level unrecognized_name alert.
	hello := minimalClientHello(t, "foo.other.test")
	_, err = raw.Write(hello)
	require.NoError(t, err)

	_ = raw.SetReadDeadline(time.Now().Add(3 * time.Second))
	alert := make([]byte, 7)
	_, err = io.ReadFull(raw, alert)
	require.NoError(t, err)

	want := []byte{0x15, 0x03, 0x03, 0x00, 0x02, 0x01, 0x70}
	assert.Equal(t, want, alert,
		"server must emit the 7-byte unrecognized_name alert record")
}

// TestGuestOriginHTTPS_SNIMux_PeekTimeout opens a TCP conn, writes
// nothing, and asserts the server closes it inside the 5s peek
// deadline (bounded between 4s and 7s to allow some slack) while
// the listener keeps accepting new, well-formed connections.
func TestGuestOriginHTTPS_SNIMux_PeekTimeout(t *testing.T) {
	cfg := identity.Prod
	_, _, httpsPort, cleanup := buildHairpinPair(t, cfg, "projA", "bodyA")
	defer cleanup()

	addr := fmt.Sprintf("127.0.0.1:%d", httpsPort)
	stuck, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer stuck.Close()

	start := time.Now()
	_, err = stuck.Read(make([]byte, 1))
	elapsed := time.Since(start)
	require.Error(t, err, "server must close the stalled conn after the peek deadline")
	assert.GreaterOrEqual(t, elapsed, 4*time.Second,
		"peek-timeout fired too early: %v", elapsed)
	assert.LessOrEqual(t, elapsed, 7*time.Second,
		"peek-timeout fired too late: %v", elapsed)

	// The accept loop must still be live: a fresh, correctly-formed
	// TLS dial on this project's own SNI succeeds.
	sni := "api.projA." + cfg.TLD
	tc, err := tls.Dial("tcp", addr, &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: true,
	})
	require.NoError(t, err,
		"accept loop must not be blocked by a stalled peek")
	tc.Close()
}

// TestGuestOriginHTTPS_SNIMux_PeerDies_SpliceDialFails_AlertNotHang
// closes B's listener before A dials, points A's peer map at a dead
// address, and asserts A's SNI mux sends the same unrecognized_name
// alert on splice-dial failure (one failure mode, not two) and
// returns within a bounded time rather than hanging.
func TestGuestOriginHTTPS_SNIMux_PeerDies_SpliceDialFails_AlertNotHang(t *testing.T) {
	cfg := identity.Prod
	proxyA, _, httpsPortA, cleanupA := buildHairpinPair(t, cfg, "projA", "bodyA")
	defer cleanupA()

	// Bind an ephemeral port, record it, then close so a Dial to it
	// yields a reliable connection-refused instead of relying on a
	// known-dead address.
	deadLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	deadAddr := deadLn.Addr().String()
	require.NoError(t, deadLn.Close())

	peerBSuffix := "projB." + cfg.TLD
	proxyA.peerHairpinsFor = func(string) map[string]string {
		return map[string]string{peerBSuffix: deadAddr}
	}

	addrA := fmt.Sprintf("127.0.0.1:%d", httpsPortA)
	raw, err := net.Dial("tcp", addrA)
	require.NoError(t, err)
	defer raw.Close()

	hello := minimalClientHello(t, "api."+peerBSuffix)
	_, err = raw.Write(hello)
	require.NoError(t, err)

	start := time.Now()
	_ = raw.SetReadDeadline(time.Now().Add(hairpinPeekTimeout + 2*time.Second))
	alert := make([]byte, 7)
	_, err = io.ReadFull(raw, alert)
	elapsed := time.Since(start)
	require.NoError(t, err, "server must emit an alert, not hang")
	assert.Less(t, elapsed, hairpinPeekTimeout+2*time.Second,
		"peer-dial-failed path must fail fast, not hang: %v", elapsed)

	want := []byte{0x15, 0x03, 0x03, 0x00, 0x02, 0x01, 0x70}
	assert.Equal(t, want, alert,
		"splice-dial failure must surface the same alert as unknown SNI")
}

// TestGuestOriginHTTP_HostMux_OwnDomainStillWorks pins that a plain
// HTTP request whose Host matches this project's own route resolves
// through the pre-existing reverse-proxy path (serveOwn).
func TestGuestOriginHTTP_HostMux_OwnDomainStillWorks(t *testing.T) {
	cfg := identity.Prod
	_, httpPort, _, cleanup := buildHairpinPair(t, cfg, "projA", "bodyA")
	defer cleanup()

	host := "api.projA." + cfg.TLD
	url := fmt.Sprintf("http://127.0.0.1:%d/", httpPort)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Host = host

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "bodyA", string(body),
		"own-project Host must flow through the reverse-proxy path")
}

// TestGuestOriginHTTP_HostMux_PeerSplice stands up two hairpin pairs
// A and B, declares them as HTTP peers of one another, then issues a
// plain HTTP GET to A with Host = api.projB.<tld>. A's host mux must
// hijack and splice to B's HTTP listener, which parses the replayed
// request and serves B's backend body.
func TestGuestOriginHTTP_HostMux_PeerSplice(t *testing.T) {
	cfg := identity.Prod
	proxyA, httpPortA, _, cleanupA := buildHairpinPair(t, cfg, "projA", "bodyA")
	defer cleanupA()
	proxyB, httpPortB, _, cleanupB := buildHairpinPair(t, cfg, "projB", "bodyB")
	defer cleanupB()

	peerBSuffix := "projB." + cfg.TLD
	peerASuffix := "projA." + cfg.TLD
	addrB := fmt.Sprintf("127.0.0.1:%d", httpPortB)
	addrA := fmt.Sprintf("127.0.0.1:%d", httpPortA)

	proxyA.peerHairpinsHTTPFor = func(string) map[string]string {
		return map[string]string{peerBSuffix: addrB}
	}
	proxyB.peerHairpinsHTTPFor = func(string) map[string]string {
		return map[string]string{peerASuffix: addrA}
	}

	// Client dials A but asks for B's hostname. The round-trip proves
	// A hijacked, replayed the request to B, received B's response,
	// and bridged the body back.
	host := "api." + peerBSuffix
	url := fmt.Sprintf("http://%s/", addrA)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Host = host

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "bodyB", string(body),
		"peer splice must deliver B's body, not A's")
}

// TestGuestOriginHTTP_HostMux_UnknownHost_421 pins the mux's reject
// path: a Host that is neither own-project nor a declared peer must
// return 421 Misdirected Request.
func TestGuestOriginHTTP_HostMux_UnknownHost_421(t *testing.T) {
	cfg := identity.Prod
	_, httpPort, _, cleanup := buildHairpinPair(t, cfg, "projA", "bodyA")
	defer cleanup()

	url := fmt.Sprintf("http://127.0.0.1:%d/", httpPort)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Host = "foo.other.test"

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusMisdirectedRequest, resp.StatusCode,
		"unknown host must surface as 421 Misdirected Request")
}
