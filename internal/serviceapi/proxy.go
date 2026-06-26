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
	"time"

	"github.com/mdubb86/devm/internal/debuglog"
)

// ProxyServer is the daemon's HTTP+HTTPS reverse proxy. Listens
// on the provided listeners (typically launchd-inherited via
// sockact.Activate), routes by Host: header to backends from the
// in-memory Routes table.
type ProxyServer struct {
	routes *Routes
	ca     *CA
}

func NewProxyServer(routes *Routes, ca *CA) *ProxyServer {
	return &ProxyServer{routes: routes, ca: ca}
}

// Serve binds the given listeners and serves until ctx is cancelled.
// Either listener slice may be nil/empty.
func (p *ProxyServer) Serve(ctx context.Context, httpListeners, httpsListeners []net.Listener) error {
	httpSrv := &http.Server{Handler: p}
	httpsSrv := &http.Server{
		Handler: p,
		TLSConfig: &tls.Config{
			GetCertificate: p.ca.GetCertificate,
			NextProtos:     []string{"h2", "http/1.1"},
		},
	}

	errCh := make(chan error, len(httpListeners)+len(httpsListeners))
	for _, ln := range httpListeners {
		ln := ln
		debuglog.Logf("serviceapi", "proxy: HTTP listening on %s", ln.Addr())
		go func() { errCh <- httpSrv.Serve(ln) }()
	}
	for _, ln := range httpsListeners {
		ln := ln
		debuglog.Logf("serviceapi", "proxy: HTTPS listening on %s", ln.Addr())
		go func() { errCh <- httpsSrv.ServeTLS(ln, "", "") }()
	}

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
		_ = httpsSrv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

// ServeHTTP routes by Host: to the registered backend.
func (p *ProxyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := stripPort(r.Host)
	route, ok := p.routes.Lookup(host)
	if !ok {
		write502NoRoute(w, host)
		return
	}
	target, _ := url.Parse(fmt.Sprintf("http://localhost:%d", route.BackendPort))
	rev := httputil.NewSingleHostReverseProxy(target)
	rev.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		write502BackendDown(w, host, route.BackendPort, err)
	}
	rev.ServeHTTP(w, r)
}

func write502NoRoute(w http.ResponseWriter, host string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	fmt.Fprintf(w, "devm: no route configured for %s\n\n", host)
	fmt.Fprintf(w, "to add one:\n")
	fmt.Fprintf(w, "  - declare service.hostname: %s in devm.yaml\n", host)
	fmt.Fprintf(w, "  - run `devm route local` or `devm route vm`\n")
}

func write502BackendDown(w http.ResponseWriter, host string, port int, err error) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	fmt.Fprintf(w, "devm: no service listening at %s → localhost:%s\n\n",
		host, strconv.Itoa(port))
	fmt.Fprintf(w, "is your dev server running?\n")
	fmt.Fprintf(w, "  vm mode:    `devm shell` to bring the sandbox up\n")
	fmt.Fprintf(w, "  local mode: start the process this hostname targets\n\n")
	fmt.Fprintf(w, "(%v)\n", err)
}
