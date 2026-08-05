package serviceapi

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/mdubb86/devm/internal/debuglog"
	"github.com/mdubb86/devm/internal/helper"
	"github.com/mdubb86/devm/internal/identity"
)

// LANDispatchPort is the fixed port for the shared LAN dispatcher —
// binds on 0.0.0.0 so a LAN device (or a reverse proxy like NPM on a
// separate box) can reach devm's HTTP proxy without a per-project
// loopback address. Not configurable in v0.9.6; a collision surfaces
// as a bind error from /routes/apply.
const LANDispatchPort = 42000

// ProxyServer is the daemon's HTTP+HTTPS reverse proxy. Binds one
// HTTP (:80) and one HTTPS (:443) listener per active project, on that
// project's allocated ProjectIP, via the helper. Dispatches
// by destination IP first (which project owns this connection), then
// by Host: header (which route within that project) — see ServeHTTP.
type ProxyServer struct {
	routes *Routes
	ca     *CA
	// helperClient dials this daemon's own identity's helper socket
	// (cfg.HelperSocketPath) — never a hardcoded prod path, so an e2e
	// daemon binds through the e2e helper, not prod's.
	helperClient *helper.Client

	mu      sync.Mutex
	perProj map[string]projectListeners

	// lanMu guards lanListener + lanSrv. Separate from mu because
	// /status calls that read rebindStatus also read from ProxyServer;
	// keeping lifecycle mutexes disjoint prevents accidental
	// cross-contamination.
	lanMu       sync.Mutex
	lanListener net.Listener
	lanSrv      *http.Server

	// rebindMu guards rebindStatus. Separate from mu because the
	// status is read from /status handlers on the request path, and
	// holding perProj's mu across those reads would serialize them
	// behind StartProjectListeners.
	rebindMu     sync.Mutex
	rebindStatus map[string]RebindStatus
}

// projectListeners is the pair of listeners (and their http.Servers,
// so Shutdown can be called) bound for one project.
type projectListeners struct {
	http     net.Listener
	https    net.Listener
	httpSrv  *http.Server
	httpsSrv *http.Server

	guestHTTP      net.Listener
	guestHTTPS     net.Listener
	guestHTTPSrv   *http.Server
	guestHTTPSSrv  *http.Server
	guestHTTPPort  int
	guestHTTPSPort int
}

// RebindState is one of the outcomes of the daemon-startup rebind pass
// (runner.go's restart-adopt loop). Read by /status to surface a
// stuck project to the user instead of silent limbo.
type RebindState string

const (
	RebindNotAttempted RebindState = "not_attempted"
	RebindPending      RebindState = "pending"
	RebindOK           RebindState = "ok"
	RebindFailed       RebindState = "failed"
)

// RebindStatus is the per-project outcome of the startup rebind pass.
// Attempts is the number of StartProjectListeners calls made (retries
// increment). LastError is the final error string when State ==
// RebindFailed; empty otherwise.
type RebindStatus struct {
	State     RebindState
	Attempts  int
	LastError string
}

func NewProxyServer(cfg identity.Config, routes *Routes, ca *CA) *ProxyServer {
	return &ProxyServer{
		routes:       routes,
		ca:           ca,
		helperClient: helper.NewClient(cfg),
		perProj:      make(map[string]projectListeners),
		rebindStatus: make(map[string]RebindStatus),
	}
}

// StartProjectListeners opens :80 and :443 listeners on projectIP via
// the helper and starts serving on them. Idempotent: a
// project that already has listeners registered is left untouched —
// callers should StopProjectListeners first if they want to rebind.
//
// The guard checks httpSrv specifically, not mere presence of a
// perProj entry — StartGuestOriginListeners can populate that entry
// independently (e.g. it succeeds on a retry where this call
// previously failed), and an entry-presence check would then skip the
// ingress bind forever.
func (p *ProxyServer) StartProjectListeners(ctx context.Context, projectID, projectIP string) error {
	p.mu.Lock()
	if pl, ok := p.perProj[projectID]; ok && pl.httpSrv != nil {
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()

	httpLn, err := p.helperClient.BindTCP(projectIP, 80)
	if err != nil {
		return fmt.Errorf("bind :80 on %s: %w", projectIP, err)
	}
	httpsLn, err := p.helperClient.BindTCP(projectIP, 443)
	if err != nil {
		httpLn.Close()
		return fmt.Errorf("bind :443 on %s: %w", projectIP, err)
	}

	httpSrv := &http.Server{Handler: p, ConnContext: p.stampLocalAddr}
	httpsSrv := &http.Server{
		Handler:     p,
		ConnContext: p.stampLocalAddr,
		TLSConfig: &tls.Config{
			GetCertificate: p.ca.GetCertificate,
			NextProtos:     []string{"h2", "http/1.1"},
		},
	}

	debuglog.Logf("serviceapi", "proxy: HTTP listening on %s (project %s)", httpLn.Addr(), projectID)
	go func() {
		if err := httpSrv.Serve(httpLn); err != nil && err != http.ErrServerClosed {
			debuglog.Logf("serviceapi", "proxy: HTTP serve for %s: %v", projectID, err)
		}
	}()
	debuglog.Logf("serviceapi", "proxy: HTTPS listening on %s (project %s)", httpsLn.Addr(), projectID)
	go func() {
		if err := httpsSrv.ServeTLS(httpsLn, "", ""); err != nil && err != http.ErrServerClosed {
			debuglog.Logf("serviceapi", "proxy: HTTPS serve for %s: %v", projectID, err)
		}
	}()

	p.recordProjectListeners(projectID, httpLn, httpsLn, httpSrv, httpsSrv)
	return nil
}

// StopProjectListeners closes the given project's HTTP/HTTPS listeners
// (if any). Idempotent — a project with no registered listeners is a
// no-op.
func (p *ProxyServer) StopProjectListeners(projectID string) {
	pl, ok := p.takeProjectListeners(projectID)

	// Clear the recorded rebind outcome unconditionally (even when
	// there were no live listeners to shut down — a rebind can fail
	// before ever populating perProj) so a torn-down project doesn't
	// leak forever in rebindStatus and a later /status read doesn't
	// report a stale RebindOK/RebindFailed for a project that no
	// longer exists.
	p.rebindMu.Lock()
	delete(p.rebindStatus, projectID)
	p.rebindMu.Unlock()

	if !ok {
		return
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if pl.httpSrv != nil {
		_ = pl.httpSrv.Shutdown(shutdownCtx)
	}
	if pl.httpsSrv != nil {
		_ = pl.httpsSrv.Shutdown(shutdownCtx)
	}
	// Shut down via the *http.Server when one was recorded — Shutdown
	// closes the listener it's serving on. Fall back to closing the raw
	// listener directly when it isn't: guestHTTP/guestHTTPS are recorded
	// at bind time, before the *http.Server goroutine's Serve call is
	// even known to have started, so a listener can be present here with
	// no server ever having been recorded for it. Without this fallback
	// that fd would never close.
	if pl.guestHTTPSrv != nil {
		_ = pl.guestHTTPSrv.Shutdown(shutdownCtx)
	} else if pl.guestHTTP != nil {
		_ = pl.guestHTTP.Close()
	}
	if pl.guestHTTPSSrv != nil {
		_ = pl.guestHTTPSSrv.Shutdown(shutdownCtx)
	} else if pl.guestHTTPS != nil {
		_ = pl.guestHTTPS.Close()
	}
}

// StopAll closes every project's listeners. Called on daemon shutdown
// so a graceful exit doesn't leak bound ports.
func (p *ProxyServer) StopAll() {
	p.mu.Lock()
	ids := make([]string, 0, len(p.perProj))
	for id := range p.perProj {
		ids = append(ids, id)
	}
	p.mu.Unlock()
	for _, id := range ids {
		p.StopProjectListeners(id)
	}
}

func (p *ProxyServer) recordProjectListeners(projectID string, httpLn, httpsLn net.Listener, httpSrv, httpsSrv *http.Server) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pl := p.perProj[projectID]
	pl.http = httpLn
	pl.https = httpsLn
	pl.httpSrv = httpSrv
	pl.httpsSrv = httpsSrv
	p.perProj[projectID] = pl
}

func (p *ProxyServer) recordGuestOriginListeners(projectID string, httpLn, httpsLn net.Listener, httpSrv, httpsSrv *http.Server, httpPort, httpsPort int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pl := p.perProj[projectID]
	pl.guestHTTP = httpLn
	pl.guestHTTPS = httpsLn
	pl.guestHTTPSrv = httpSrv
	pl.guestHTTPSSrv = httpsSrv
	pl.guestHTTPPort = httpPort
	pl.guestHTTPSPort = httpsPort
	p.perProj[projectID] = pl
}

func (p *ProxyServer) takeProjectListeners(projectID string) (projectListeners, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pl, ok := p.perProj[projectID]
	if ok {
		delete(p.perProj, projectID)
	}
	return pl, ok
}

// RecordRebindStatus stores the outcome of the startup rebind pass
// for projectID. Called by the runner's rebind loop; read by /status.
func (p *ProxyServer) RecordRebindStatus(projectID string, s RebindStatus) {
	p.rebindMu.Lock()
	defer p.rebindMu.Unlock()
	p.rebindStatus[projectID] = s
}

// RebindStatus returns the recorded outcome for projectID. The second
// return is false when no rebind was attempted (e.g. the project
// wasn't recovered by AdoptIronProxies).
func (p *ProxyServer) RebindStatus(projectID string) (RebindStatus, bool) {
	p.rebindMu.Lock()
	defer p.rebindMu.Unlock()
	s, ok := p.rebindStatus[projectID]
	return s, ok
}

type ctxKey int

const (
	ctxKeyLocalAddr ctxKey = iota
)

// stampLocalAddr is the http.Server ConnContext hook: it stamps the
// accepted connection's local address (the project IP the client
// dialed) into the request context so ServeHTTP can dispatch by
// destination IP.
func (p *ProxyServer) stampLocalAddr(ctx context.Context, c net.Conn) context.Context {
	return context.WithValue(ctx, ctxKeyLocalAddr, c.LocalAddr())
}

func localAddrFromCtx(ctx context.Context) (net.IP, bool) {
	v := ctx.Value(ctxKeyLocalAddr)
	if v == nil {
		return nil, false
	}
	if ta, ok := v.(*net.TCPAddr); ok {
		return ta.IP, true
	}
	return nil, false
}

// ServeHTTP dispatches by destination IP first (which project owns
// this connection), then by Host: header (which route within that
// project). A Host that doesn't belong to the dest-IP's project is a
// 502, never a fall-through to another project — this is the
// isolation guarantee.
func (p *ProxyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ip, ok := localAddrFromCtx(r.Context())
	if !ok {
		write502NoRoute(w, r.Host)
		return
	}
	project := projectByIP(ip.String())
	if project == "" {
		write502NoProject(w, ip.String())
		return
	}
	host := stripPort(r.Host)
	route, ok := p.routes.Lookup(host, project)
	if !ok {
		write502NoRoute(w, host)
		return
	}
	p.dispatch(w, r, route)
}

// dispatch is the shared reverse-proxy mechanics — dial the route's
// backend and copy the response — factored out of ServeHTTP so
// serveLAN (LAN dispatch, Host-header only, no per-project dest-IP
// scope) can reuse it instead of duplicating the reverse-proxy setup.
func (p *ProxyServer) dispatch(w http.ResponseWriter, r *http.Request, route Route) {
	host := stripPort(r.Host)
	backendHost := route.BackendHost
	if backendHost == "" {
		backendHost = "localhost"
	}
	target, _ := url.Parse(fmt.Sprintf("http://%s:%d", backendHost, route.BackendPort))
	rev := httputil.NewSingleHostReverseProxy(target)
	rev.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		write502BackendDown(w, host, backendHost, route.BackendPort, err)
	}
	rev.ServeHTTP(w, r)
}

// StartLANListener binds 0.0.0.0:lanPort and starts serving the shared
// LAN dispatcher. Idempotent — a second call while already bound
// returns nil. Called by reconcileLAN when the first ExposeHost route
// enters the table.
func (p *ProxyServer) StartLANListener(ctx context.Context, lanPort int) error {
	p.lanMu.Lock()
	defer p.lanMu.Unlock()
	if p.lanListener != nil {
		return nil
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", lanPort))
	if err != nil {
		return fmt.Errorf("bind LAN listener :%d: %w", lanPort, err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(p.serveLAN)}
	p.lanListener = ln
	p.lanSrv = srv
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			debuglog.Logf("serviceapi", "LAN listener serve: %v", err)
		}
	}()
	debuglog.Logf("serviceapi", "LAN listener bound on 0.0.0.0:%d", lanPort)
	return nil
}

// StopLANListener closes the LAN listener. Idempotent — a second call
// when not bound is a no-op. Called when the last ExposeHost route
// leaves the table.
func (p *ProxyServer) StopLANListener() {
	p.lanMu.Lock()
	defer p.lanMu.Unlock()
	if p.lanSrv == nil {
		return
	}
	_ = p.lanSrv.Shutdown(context.Background())
	p.lanListener = nil
	p.lanSrv = nil
}

// serveLAN dispatches a LAN request by Host header only — no
// per-project dest-IP scope filter, since the destination IP here is
// the Mac's shared LAN interface, not a per-project loopback address.
func (p *ProxyServer) serveLAN(w http.ResponseWriter, r *http.Request) {
	host := stripPort(r.Host)
	route, ok := p.routes.LANLookup(host)
	if !ok {
		write502NoRoute(w, host)
		return
	}
	p.dispatch(w, r, route)
}

// projectByIP reverse-maps an IP string to the projectID that owns it.
// Reads ironProxyState. Returns "" when no project claims the IP.
func projectByIP(ip string) string {
	for _, id := range ironProxyState.keys() {
		if info, ok := ironProxyState.get(id); ok && info.ProjectIP == ip {
			return id
		}
	}
	return ""
}

func write502NoRoute(w http.ResponseWriter, host string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	fmt.Fprintf(w, "devm: no route configured for %s\n\n", host)
	fmt.Fprintf(w, "to add one:\n")
	fmt.Fprintf(w, "  - declare service.hostname: %s in devm.yaml\n", host)
	fmt.Fprintf(w, "  - run `devm route local` or `devm route vm`\n")
}

func write502NoProject(w http.ResponseWriter, ip string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	fmt.Fprintf(w, "devm: no project bound at %s\n\ndid a project just get torn down?\n", ip)
}

func write502BackendDown(w http.ResponseWriter, host, backendHost string, port int, err error) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	fmt.Fprintf(w, "devm: no service listening at %s → %s:%s\n\n",
		host, backendHost, strconv.Itoa(port))
	fmt.Fprintf(w, "is your dev server running?\n")
	fmt.Fprintf(w, "  vm mode:    `devm shell` to bring the sandbox up\n")
	fmt.Fprintf(w, "  local mode: start the process this hostname targets\n\n")
	fmt.Fprintf(w, "(%v)\n", err)
}
