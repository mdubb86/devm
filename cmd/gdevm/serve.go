package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// defaultServeAddr binds every interface, not just loopback: softnet
// forwards the Mac-side reserved health probe (see internal/serviceapi's
// computeExposeMap for port 8940) to the guest's external interface, so
// loopback-only would accept the TCP handshake at the kernel and then
// EOF because gdevm serve isn't listening there. Guest isolation is
// provided by softnet's egress firewall — the port never leaves the
// per-project sandbox.
const defaultServeAddr = "0.0.0.0:8940"

func serveAddr() string {
	if a := os.Getenv("DEVM_GDEVM_SERVE_ADDR"); a != "" {
		return a
	}
	return defaultServeAddr
}

// buildServeHandler composes the gdevm serve top-level handler. When
// tld is empty the preview route is disabled: every request goes to the
// health mux, which only answers /v1/health.
func buildServeHandler(started time.Time, tld string) http.Handler {
	return buildServeHandlerWithRoot(started, tld, "/")
}

// buildServeHandlerWithRoot is buildServeHandler with the preview file
// root injectable, so tests don't depend on the real guest filesystem.
func buildServeHandlerWithRoot(started time.Time, tld, root string) http.Handler {
	healthMux := http.NewServeMux()
	healthMux.Handle("/v1/health", healthHandler(started))

	if tld == "" {
		return healthMux
	}

	preview := previewFileServerFromRoot(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPreviewHost(r.Host, tld) {
			preview.ServeHTTP(w, r)
			return
		}
		healthMux.ServeHTTP(w, r)
	})
}

// serveMain starts gdevm's guest-side daemon: a long-running process
// bound to a loopback address, serving the v1 HTTP API, until it
// receives SIGTERM/SIGINT. Returns 0 on clean shutdown, 1 on bind
// failure, 2 on bad args.
func serveMain(args []string) int {
	// Reject any args — this subcommand takes none in v1.
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "gdevm serve: no arguments accepted")
		return 2
	}

	started := time.Now()

	// Register the shutdown signal handler before the listener binds,
	// and therefore before the bound address becomes observable to
	// tests or a watchdog. If Notify ran after net.Listen, a
	// SIGTERM/SIGINT arriving in that window would hit OS-default
	// disposition and kill the process outright instead of triggering
	// graceful shutdown.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)

	ln, err := net.Listen("tcp", serveAddr())
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm serve: bind %s: %v\n", serveAddr(), err)
		return 1
	}
	setServeAddrForTest(ln.Addr().String())
	defer setServeAddrForTest("")
	if addrPublishedHookForTest != nil {
		addrPublishedHookForTest()
	}

	srv := &http.Server{Handler: buildServeHandler(started, os.Getenv("DEVM_TLD"))}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	<-sig

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "gdevm serve: shutdown: %v\n", err)
	}
	<-errCh
	return 0
}
