package serviceapi

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// hairpinPeekTimeout bounds how long the SNI mux will wait for a
// ClientHello on an accepted raw conn. Anything slower is a slowloris
// (or a mis-dialed port) and gets closed without a TLS alert.
const hairpinPeekTimeout = 5 * time.Second

// hairpinSNILogWindow bounds how often an identical SNI rejection or
// peer-splice dial failure re-logs. One minute matches softnet's
// egressLogWindow — a hot-loop guest probe keeps a per-minute
// heartbeat without drowning the log.
const hairpinSNILogWindow = 60 * time.Second

// hairpinSNILog dedupes the SNI mux's info-level rejections and
// splice-dial failures. Package-scope so it survives across every
// StartGuestOriginListeners invocation in a daemon.
var hairpinSNILog = newServiceapiDedupLogger(hairpinSNILogWindow)

// hairpinDispatchHTTPS runs the ClientHello peek + three-way dispatch
// on one raw conn accepted from a guest-origin HTTPS listener.
//
// Three paths:
//   - SNI falls under this project's own zone (`.<projectID>.<tld>`):
//     terminate TLS with the daemon's own cert via httpsSrv.TLSConfig
//     and serve the request with h, mirroring the pre-mux ServeTLS
//     behavior for own-project traffic.
//   - SNI falls under a declared peer's zone: dial the peer's own
//     guest-origin HTTPS listener on Mac loopback and splice raw
//     bytes. The peer's listener terminates TLS with its own cert;
//     this project never mints a leaf for a peer's hostname.
//   - Anything else (unparseable, unknown SNI, empty SNI, peer dial
//     failure): send a TLS unrecognized_name alert record and close.
//
// Fail-closed on every unknown case. The one place a successful splice
// returns is the peer-match arm.
func (p *ProxyServer) hairpinDispatchHTTPS(raw net.Conn, h http.Handler, httpsSrv *http.Server, lnAddr net.Addr, projectID string) {
	_ = raw.SetReadDeadline(time.Now().Add(hairpinPeekTimeout))
	br := bufio.NewReader(raw)
	hdr, err := br.Peek(5)
	if err != nil {
		// Peek timeout (slowloris) or a short conn that never sent a
		// record. Close without alert — we never saw a ClientHello.
		_ = raw.Close()
		return
	}
	if hdr[0] != 0x16 {
		// Not a TLS handshake record. Close silently — a legitimate
		// client would not land here.
		_ = raw.Close()
		return
	}
	recordLen := int(binary.BigEndian.Uint16(hdr[3:5]))
	full, err := br.Peek(5 + recordLen)
	if err != nil {
		_ = raw.Close()
		return
	}
	// ClientHello is in hand; the deadline served its slowloris
	// purpose. Clear it so the subsequent TLS termination or raw
	// splice runs under its own timeouts.
	_ = raw.SetReadDeadline(time.Time{})

	sni, err := parseSNI(full)
	if err != nil {
		hairpinSNILog.Logf(projectID+"|parse",
			"hairpin: sni mux parse-error project=%s remote=%s: %v",
			projectID, raw.RemoteAddr(), err)
		sendUnrecognizedNameAlert(raw)
		_ = raw.Close()
		return
	}

	// Capture everything bufio actually read from raw, not just the
	// first record. For well-behaved TLS clients bufio has exactly the
	// ClientHello here (the client is waiting on ServerHello before
	// writing more), but a client that blasted additional bytes into
	// the same TCP segment would otherwise lose them when prefixConn
	// / backend.Write replays only the record. Draining br.Buffered()
	// guarantees everything we consumed from raw is replayed.
	peeked := readPeeked(br, br.Buffered())

	tld := p.cfg.TLD
	ownZone := projectID + "." + tld
	if sni != "" && (sni == ownZone || strings.HasSuffix(sni, "."+ownZone)) {
		// Own-project SNI: terminate TLS and serve HTTP through h,
		// preserving the pre-mux behavior for this project's own
		// traffic. The one-shot listener feeds exactly this tls.Conn
		// to http.Server.Serve; the server handles the full
		// keep-alive lifecycle (one HTTP request or many) on its own
		// goroutine. Serve returns when Accept errors on the second
		// call — the per-conn goroutine runs independently until the
		// client closes.
		pc := &prefixConn{prefix: peeked, Conn: raw}
		srv := &http.Server{
			Handler:   h,
			TLSConfig: httpsSrv.TLSConfig,
			ErrorLog:  httpsSrv.ErrorLog,
		}
		tlsConn := tls.Server(pc, httpsSrv.TLSConfig)
		_ = srv.Serve(singleConnListener(tlsConn, lnAddr))
		return
	}

	peerKey := peerSuffix(sni, tld)
	if peerKey != "" {
		if peer, ok := p.peerHairpinsFor(projectID)[peerKey]; ok && peer != "" {
			backend, derr := net.DialTimeout("tcp", peer, hairpinPeekTimeout)
			if derr != nil {
				hairpinSNILog.Logf(projectID+"|"+peerKey+"|dial",
					"hairpin: sni mux peer dial failed project=%s sni=%s peer=%s: %v",
					projectID, sni, peer, derr)
				sendUnrecognizedNameAlert(raw)
				_ = raw.Close()
				return
			}
			if _, werr := backend.Write(peeked); werr != nil {
				_ = backend.Close()
				_ = raw.Close()
				return
			}
			spliceConns(raw, backend)
			return
		}
	}

	hairpinSNILog.Logf(projectID+"|"+sni,
		"hairpin: sni mux reject sni=%q from=%s project=%s",
		sni, raw.RemoteAddr(), projectID)
	sendUnrecognizedNameAlert(raw)
	_ = raw.Close()
}

// peerSuffix extracts the "<project>.<tld>" key that a sibling SNI
// resolves to. Returns "" when sni doesn't live under tld — the SNI
// is for a different TLD and could not match any declared peer.
//
// Behaviour:
//   - "api.sibling.test"      + tld "test"     -> "sibling.test"
//   - "sibling.test"          + tld "test"     -> "sibling.test"
//   - "app.sub.sibling.test"  + tld "test"     -> "sibling.test"
//     (the suffix is always the last label plus the TLD; nested
//     subdomains collapse to the project boundary)
//   - "api.sibling.e2e.test"  + tld "e2e.test" -> "sibling.e2e.test"
//   - "foo.other"             + tld "test"     -> ""
//
// Case is preserved; callers of the lookup should pass already-canonical
// keys (config-load lowercases).
func peerSuffix(sni, tld string) string {
	if sni == "" || tld == "" {
		return ""
	}
	if sni == tld {
		// Degenerate: the SNI is the bare TLD. There is no project
		// label to key on; treat as no-match. (Nothing in the daemon's
		// own code reaches here; included so a crafted ClientHello
		// cannot sneak through as a "peer" with empty key.)
		return ""
	}
	if !strings.HasSuffix(sni, "."+tld) {
		return ""
	}
	head := strings.TrimSuffix(sni, "."+tld)
	if i := strings.LastIndexByte(head, '.'); i >= 0 {
		head = head[i+1:]
	}
	if head == "" {
		return ""
	}
	return head + "." + tld
}

// sendUnrecognizedNameAlert writes an RFC 6066 §3 unrecognized_name
// warning alert. One TLS record: content type 0x15 (alert), version
// 0x0303 (TLS 1.2 — matches what any modern client already saw in the
// ServerHello had we reached it), length 0x0002, body (0x01, 0x70).
//
// Return value is ignored on purpose: a write failure here just means
// the peer disconnected first; we're closing the conn next either way.
func sendUnrecognizedNameAlert(c net.Conn) {
	_, _ = c.Write([]byte{0x15, 0x03, 0x03, 0x00, 0x02, 0x01, 0x70})
}

// readPeeked returns the first n bytes already in br's buffer as a
// fresh slice. Does not Discard — the bytes stay in the reader so a
// caller that also wraps the underlying conn (via prefixConn) sees a
// consistent view. Callers who have moved past bufio.Reader into
// another wrapper don't need to re-read br again.
func readPeeked(br *bufio.Reader, n int) []byte {
	src, _ := br.Peek(n)
	out := make([]byte, len(src))
	copy(out, src)
	return out
}

// spliceConns copies bytes bidirectionally between a and b with
// half-close on each direction. Mirrors internal/softnet/egress.go's
// splice/halfClose; duplicated here deliberately — serviceapi must not
// import softnet (the daemon's package layering is one-way: softnet
// below serviceapi).
func spliceConns(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(b, a)
		spliceHalfClose(b, a)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(a, b)
		spliceHalfClose(a, b)
	}()
	wg.Wait()
	_ = a.Close()
	_ = b.Close()
}

func spliceHalfClose(dst, src net.Conn) {
	if c, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = c.CloseWrite()
	}
	if c, ok := src.(interface{ CloseRead() error }); ok {
		_ = c.CloseRead()
	}
}

// singleConnListener returns a net.Listener that yields c on the first
// Accept and returns net.ErrClosed on every subsequent call. Addr
// reports addr (the underlying bound listener's address) so the
// http.Server stamps the right LocalAddr on its request contexts.
//
// Pattern used to bridge a conn pulled out of a raw accept loop into
// http.Server.Serve for one-shot handling. The returned listener's
// Close is a no-op — the http.Server already closes the conn when the
// request lifecycle ends; a second close on the raw conn is harmless.
func singleConnListener(c net.Conn, addr net.Addr) net.Listener {
	return &oneShotListener{conn: c, addr: addr}
}

type oneShotListener struct {
	mu       sync.Mutex
	conn     net.Conn
	addr     net.Addr
	accepted bool
}

func (l *oneShotListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.accepted {
		return nil, net.ErrClosed
	}
	l.accepted = true
	return l.conn, nil
}

func (l *oneShotListener) Close() error { return nil }

func (l *oneShotListener) Addr() net.Addr { return l.addr }

// serviceapiDedupLogger — same shape as softnet's dedupLogger, except
// output goes to the daemon's stdlib log (routed to the daemon's
// stdout log via log.Printf) rather than softnet's own stderr sink.
// Duplicated instead of imported for the same one-way-import reason
// as spliceConns above.
type serviceapiDedupLogger struct {
	mu     sync.Mutex
	state  map[string]*serviceapiDedupEntry
	window time.Duration
	now    func() time.Time
}

type serviceapiDedupEntry struct {
	lastLoggedAt time.Time
	suppressed   int
}

func newServiceapiDedupLogger(window time.Duration) *serviceapiDedupLogger {
	return &serviceapiDedupLogger{
		state:  make(map[string]*serviceapiDedupEntry),
		window: window,
		now:    time.Now,
	}
}

func (d *serviceapiDedupLogger) Logf(key, format string, args ...any) {
	emit, suppressed := d.check(key)
	if !emit {
		return
	}
	msg := fmt.Sprintf(format, args...)
	if suppressed > 0 {
		log.Printf("%s (repeated %dx in last %s)", msg, suppressed, d.window)
	} else {
		log.Print(msg)
	}
}

func (d *serviceapiDedupLogger) check(key string) (emit bool, suppressed int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	s, ok := d.state[key]
	if !ok {
		d.state[key] = &serviceapiDedupEntry{lastLoggedAt: now}
		return true, 0
	}
	if now.Sub(s.lastLoggedAt) >= d.window {
		n := s.suppressed
		s.lastLoggedAt = now
		s.suppressed = 0
		return true, n
	}
	s.suppressed++
	return false, 0
}
