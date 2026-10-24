package serviceapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"time"

	"github.com/mdubb86/devm/internal/daemonlog"
)

// guestOriginBackend resolves a Host header from guest-originated `.test`
// traffic to the address the guest-origin listener dials.
//
// The backend is always this project's own guest at projectIP:<service-port>,
// never the route's BackendHost. That pin is deliberate and load-bearing: a
// project in `devm route local` mode has Mac-local backends, and honoring them
// here would hand the in-guest agent reachability to services on the Mac that
// it otherwise has no path to. The guest-origin listener exists to let the
// guest reach its own services over TLS and nothing else.
//
// Direct services are excluded by Routes.Lookup — they stay raw TCP
// end-to-end; softnet's .test DNS answers loopback for them and their
// traffic never leaves the guest.
func guestOriginBackend(routes *Routes, host, projectID, projectIP string) (string, bool) {
	if projectID == "" || projectIP == "" {
		return "", false
	}
	route, ok := routes.Lookup(stripPort(host), projectID)
	if !ok {
		return "", false
	}
	// :80 and :443 on projectIP are the browser-facing ProxyServer's own
	// listeners (StartProjectListeners), never a guest service's — a
	// non-direct service declared with port: 80 or port: 443 must not
	// resolve here, or guest traffic would be reflected into that
	// listener, whose dispatch honors route.BackendHost (Mac localhost
	// in route-local mode) instead of staying guest-pinned.
	if route.BackendPort == 80 || route.BackendPort == 443 {
		return "", false
	}
	return net.JoinHostPort(projectIP, strconv.Itoa(route.BackendPort)), true
}

// guestOriginHandler serves guest-originated `.test` traffic for one project.
// Unlike ProxyServer.ServeHTTP it needs no destination-IP dispatch: the
// listener is per-project, so the project is fixed at construction.
//
// Three-way dispatch mirrors the HTTPS SNI mux in hairpin_sni.go:
//   - Own project Host → serveOwn (existing reverse-proxy path).
//   - Declared peer Host → hijack and splice to the peer's own
//     GuestHTTPPort on Mac loopback. The peer's listener parses the
//     request and dispatches it as its own guest would.
//   - Unknown Host → 421 Misdirected Request.
type guestOriginHandler struct {
	routes              *Routes
	projectID           string
	projectIP           string
	tld                 string
	peerHairpinsHTTPFor func() map[string]string
}

const (
	// hairpinHTTPSpliceTimeout bounds the peer-backend dial so a dead
	// peer's host-mux attempt fails fast instead of hanging the
	// client. Matches hairpinPeekTimeout in hairpin_sni.go.
	hairpinHTTPSpliceTimeout = 5 * time.Second

	// hairpinHTTPMaxBodyBytes caps the request body captured before
	// the Hijack. Beyond this the handler returns 413 — a dev-tool
	// ceiling for peer-splice bodies, chosen above typical POST
	// payloads (JSON APIs, multipart uploads) but well under memory
	// that would meaningfully pressure the daemon. Streaming the
	// body post-hijack is not an option: the http.Hijacker contract
	// forbids reading r.Body after Hijack returns.
	hairpinHTTPMaxBodyBytes = 32 << 20
)

func (h *guestOriginHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := stripPort(r.Host)
	if _, ok := guestOriginBackend(h.routes, r.Host, h.projectID, h.projectIP); ok {
		h.serveOwn(w, r)
		return
	}
	if peer, ok := h.peerHairpinsHTTPFor()[peerSuffix(host, h.tld)]; ok && peer != "" {
		h.spliceToPeer(w, r, peer)
		return
	}
	hairpinSNILog.Logf(h.projectID+"|"+host+"|reject",
		"hairpin: host mux reject host=%q project=%s", host, h.projectID)
	http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
}

func (h *guestOriginHandler) serveOwn(w http.ResponseWriter, r *http.Request) {
	host := stripPort(r.Host)
	// guestOriginBackend is re-run here even though the dispatcher
	// already called it: ok is guaranteed, but we need the backend
	// address itself, and the second call is cheap compared to
	// threading the value through a dispatcher that otherwise ignores
	// it.
	backend, _ := guestOriginBackend(h.routes, r.Host, h.projectID, h.projectIP)
	target, _ := url.Parse("http://" + backend)
	rev := httputil.NewSingleHostReverseProxy(target)
	rev.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, "devm: no service listening at %s → %s\n\n", host, backend)
		fmt.Fprintf(w, "is the service running inside the VM?\n\n(%v)\n", err)
	}
	rev.ServeHTTP(w, r)
}

// spliceToPeer proxies one HTTP request to the peer's own listener
// by replaying the parsed request and splicing the response plus any
// subsequent bytes. The order of operations here is load-bearing:
//
//  1. Read the request body before Hijack. The http.Hijacker contract
//     explicitly forbids using r.Body after Hijack returns — the
//     server has released the conn and no longer manages the body
//     reader. Reading it here also triggers an auto-100-Continue
//     when the client used Expect: 100-continue.
//  2. Dial the peer before Hijack. A dial failure here can still
//     surface as a plain 502 through the response writer; after
//     Hijack we would only be able to RST the client with no
//     diagnostic.
//  3. Set r.Close = true. Once the conn is spliced, the client is
//     pinned to this peer — a keep-alive next request on the same
//     conn with a different Host would land here regardless and be
//     421'd by the peer. Connection: close propagates through so
//     the client naturally closes after the response.
//  4. Hijack, write the replayed request, forward any bytes bufio
//     had already read from the client (pipelined request not yet
//     consumed), then bidirectional splice.
func (h *guestOriginHandler) spliceToPeer(w http.ResponseWriter, r *http.Request, peerAddr string) {
	host := stripPort(r.Host)

	// 1. Capture the body before Hijack. A GET with no body reads
	//    zero bytes immediately; a POST / PUT / chunked body drains
	//    into the buffer. The 1-byte overread sentinel is how we
	//    distinguish "exactly 32MB" from "bigger than 32MB".
	body, err := io.ReadAll(io.LimitReader(r.Body, hairpinHTTPMaxBodyBytes+1))
	if err != nil {
		hairpinSNILog.Logf(h.projectID+"|"+host+"|readbody",
			"hairpin: host mux body read failed project=%s host=%s: %v",
			h.projectID, host, err)
		http.Error(w, "read body", http.StatusBadGateway)
		return
	}
	if len(body) > hairpinHTTPMaxBodyBytes {
		http.Error(w, "request entity too large", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	// Chunked framing is unneeded once the body is a fixed buffer.
	// Leaving TransferEncoding set would make r.Write re-chunk the
	// output; clearing it serializes with Content-Length instead.
	r.TransferEncoding = nil
	// Expect: 100-continue was already resolved by reading r.Body
	// above. Forwarding it would make the peer send a stray 100
	// response that bridges back to a client that doesn't expect one.
	r.Header.Del("Expect")

	// 2. Dial the peer before Hijack so we can still 502 cleanly.
	backend, err := net.DialTimeout("tcp", peerAddr, hairpinHTTPSpliceTimeout)
	if err != nil {
		hairpinSNILog.Logf(h.projectID+"|"+host+"|dial",
			"hairpin: host mux peer dial failed project=%s host=%s peer=%s: %v",
			h.projectID, host, peerAddr, err)
		http.Error(w, "peer unreachable", http.StatusBadGateway)
		return
	}
	defer backend.Close()

	// 3. Pin the client to this single request/response. See the
	//    function-level comment above (step 3) for why.
	r.Close = true

	// 4. Hijack and replay.
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "server does not support hijacking", http.StatusInternalServerError)
		return
	}
	clientConn, buf, err := hj.Hijack()
	if err != nil {
		hairpinSNILog.Logf(h.projectID+"|"+host+"|hijack",
			"hairpin: host mux hijack failed project=%s host=%s: %v",
			h.projectID, host, err)
		return
	}
	defer clientConn.Close()

	if err := r.Write(backend); err != nil {
		hairpinSNILog.Logf(h.projectID+"|"+host+"|rwrite",
			"hairpin: host mux r.Write failed project=%s host=%s: %v",
			h.projectID, host, err)
		return
	}
	// Any client bytes buffered by the hijacker (unlikely for HTTP/1.1
	// non-pipelined, possible for a client that coalesced a follow-up
	// into the same TCP write) must be forwarded before the splice
	// starts — otherwise they'd be stranded in buf.Reader forever.
	if n := buf.Reader.Buffered(); n > 0 {
		b, rerr := io.ReadAll(buf.Reader)
		if rerr != nil {
			hairpinSNILog.Logf(h.projectID+"|"+host+"|readpipe",
				"hairpin: host mux read-pipelined failed project=%s host=%s: %v",
				h.projectID, host, rerr)
			return
		}
		if _, werr := backend.Write(b); werr != nil {
			hairpinSNILog.Logf(h.projectID+"|"+host+"|writepipe",
				"hairpin: host mux write-pipelined failed project=%s host=%s: %v",
				h.projectID, host, werr)
			return
		}
	}
	spliceConns(clientConn, backend)
}

// StartGuestOriginListeners binds this project's guest-origin HTTP and HTTPS
// listeners on Mac loopback and returns the ports the kernel assigned.
// softnet dials them from the Mac side, so no helper-brokered privileged bind
// is needed. Ports are ephemeral: the daemon-restart path rebinds and
// re-pushes them to softnet together (see rebindProjectListeners).
//
// Idempotent like StartProjectListeners: a project that already has a live
// guest-origin pair is left untouched and its existing ports are returned —
// otherwise a retried /vm/start (e.g. after a CLI timeout) would rebind a
// fresh pair and orphan the previous *http.Server goroutines and fds, since
// nothing ever closes a pair that isn't reachable through perProj anymore.
func (p *ProxyServer) StartGuestOriginListeners(ctx context.Context, projectID, projectIP string) (int, int, error) {
	// The per-project bind lock (shared with StartProjectListeners —
	// see bindLocks's doc comment on ProxyServer) is held across the
	// entire check + bind + record for THIS project: releasing it in
	// between let two concurrent callers both pass the "already bound?"
	// check and each bind a fresh ephemeral pair, one of which never
	// made it into perProj and leaked (goroutines + fds) until process
	// exit. Per-project rather than p.mu itself so an unrelated
	// project's Start*Listeners call isn't serialized behind this one's
	// bind.
	unlock := p.lockProjectBind(projectID)
	defer unlock()

	p.mu.Lock()
	pl, ok := p.perProj[projectID]
	p.mu.Unlock()
	if ok && pl.guestHTTPSrv != nil {
		return pl.guestHTTPPort, pl.guestHTTPSPort, nil
	}

	h := &guestOriginHandler{
		routes:              p.routes,
		projectID:           projectID,
		projectIP:           projectIP,
		tld:                 p.cfg.TLD,
		peerHairpinsHTTPFor: func() map[string]string { return p.peerHairpinsHTTPFor(projectID) },
	}

	httpLn, err := net.Listen("tcp", ironProxyListenAddr(0))
	if err != nil {
		return 0, 0, fmt.Errorf("bind guest-origin http: %w", err)
	}
	httpsLn, err := net.Listen("tcp", ironProxyListenAddr(0))
	if err != nil {
		httpLn.Close()
		return 0, 0, fmt.Errorf("bind guest-origin https: %w", err)
	}
	httpPort := httpLn.Addr().(*net.TCPAddr).Port
	httpsPort := httpsLn.Addr().(*net.TCPAddr).Port

	httpSrv := &http.Server{Handler: h}
	httpsSrv := &http.Server{
		Handler: h,
		TLSConfig: &tls.Config{
			GetCertificate: p.ca.GetCertificate,
			NextProtos:     []string{"h2", "http/1.1"},
		},
	}

	go func() {
		if err := httpSrv.Serve(httpLn); err != nil && err != http.ErrServerClosed {
			daemonlog.Errorf("serviceapi: guest-origin HTTP serve for %s exited: %v", projectID, err)
		}
	}()
	// The HTTPS loop no longer delegates to http.Server.ServeTLS: the
	// hairpin's SNI mux needs to peek the ClientHello before deciding
	// whether to terminate TLS (own-project path) or splice raw bytes
	// to a declared peer's own listener (peer path). hairpinDispatchHTTPS
	// owns the full three-way dispatch and the ClientHello peek
	// deadline; httpsSrv's TLSConfig is reused for the own-project
	// termination so cert signing stays routed through p.ca.
	go func() {
		for {
			raw, err := httpsLn.Accept()
			if err != nil {
				if err != http.ErrServerClosed && !errors.Is(err, net.ErrClosed) {
					daemonlog.Errorf("serviceapi: guest-origin HTTPS accept for %s exited: %v", projectID, err)
				}
				return
			}
			go p.hairpinDispatchHTTPS(raw, h, httpsSrv, httpsLn.Addr(), projectID)
		}
	}()

	// Re-read the current entry rather than reusing the pl checked
	// above, mirroring StartProjectListeners: cheap insurance that the
	// merge stays correct even if a future caller ever reaches perProj
	// without going through bindLocks.
	p.mu.Lock()
	pl = p.perProj[projectID]
	pl.guestHTTP = httpLn
	pl.guestHTTPS = httpsLn
	pl.guestHTTPSrv = httpSrv
	pl.guestHTTPSSrv = httpsSrv
	pl.guestHTTPPort = httpPort
	pl.guestHTTPSPort = httpsPort
	p.perProj[projectID] = pl
	p.mu.Unlock()
	log.Printf("serviceapi: guest-origin listening on %s/%s (project %s)",
		httpLn.Addr(), httpsLn.Addr(), projectID)
	return httpPort, httpsPort, nil
}
