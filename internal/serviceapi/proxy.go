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
}

// projectListeners is the pair of listeners (and their http.Servers,
// so Shutdown can be called) bound for one project.
type projectListeners struct {
	http     net.Listener
	https    net.Listener
	httpSrv  *http.Server
	httpsSrv *http.Server
}

func NewProxyServer(cfg identity.Config, routes *Routes, ca *CA) *ProxyServer {
	return &ProxyServer{
		routes:       routes,
		ca:           ca,
		helperClient: helper.NewClient(cfg),
		perProj:      make(map[string]projectListeners),
	}
}

// StartProjectListeners opens :80 and :443 listeners on projectIP via
// the helper and starts serving on them. Idempotent: a
// project that already has listeners registered is left untouched —
// callers should StopProjectListeners first if they want to rebind.
func (p *ProxyServer) StartProjectListeners(ctx context.Context, projectID, projectIP string) error {
	p.mu.Lock()
	if _, ok := p.perProj[projectID]; ok {
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
	p.perProj[projectID] = projectListeners{http: httpLn, https: httpsLn, httpSrv: httpSrv, httpsSrv: httpsSrv}
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
