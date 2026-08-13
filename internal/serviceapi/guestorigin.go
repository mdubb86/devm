package serviceapi

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"

	"github.com/mdubb86/devm/internal/debuglog"
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
type guestOriginHandler struct {
	routes    *Routes
	projectID string
	projectIP string
}

func (h *guestOriginHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := stripPort(r.Host)
	backend, ok := guestOriginBackend(h.routes, r.Host, h.projectID, h.projectIP)
	if !ok {
		write502NoRoute(w, host)
		return
	}
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
	p.mu.Lock()
	if pl, ok := p.perProj[projectID]; ok && pl.guestHTTPSrv != nil {
		httpPort, httpsPort := pl.guestHTTPPort, pl.guestHTTPSPort
		p.mu.Unlock()
		return httpPort, httpsPort, nil
	}
	p.mu.Unlock()

	h := &guestOriginHandler{routes: p.routes, projectID: projectID, projectIP: projectIP}

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
			log.Printf("serviceapi: guest-origin HTTP serve for %s exited: %v", projectID, err)
		}
	}()
	go func() {
		if err := httpsSrv.ServeTLS(httpsLn, "", ""); err != nil && err != http.ErrServerClosed {
			log.Printf("serviceapi: guest-origin HTTPS serve for %s exited: %v", projectID, err)
		}
	}()

	p.recordGuestOriginListeners(projectID, httpLn, httpsLn, httpSrv, httpsSrv, httpPort, httpsPort)
	debuglog.Logf("serviceapi", "guest-origin listening on %s/%s (project %s)",
		httpLn.Addr(), httpsLn.Addr(), projectID)
	return httpPort, httpsPort, nil
}
