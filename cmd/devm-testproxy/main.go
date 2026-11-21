// devm-testproxy is a tiny HTTP-over-TCP -> HTTP-over-Unix-socket proxy
// used by the mac/webview Playwright e2e tests. Chromium can't dial a
// Unix socket directly; this binary bridges the gap so a real browser
// page can reach the e2e daemon at
// ~/Library/Application Support/devm-e2e/devm.sock over plain TCP.
//
// It is a pure L7 forwarder: no auth, no path rewriting, no request
// filtering. The one response addition is a CORS header — see newProxy.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: devm-testproxy <tcp-port> <unix-socket-path>")
		os.Exit(2)
	}

	port, err := strconv.Atoi(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "devm-testproxy: invalid tcp-port %q: %v\n", os.Args[1], err)
		os.Exit(2)
	}

	if err := run(port, os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "devm-testproxy:", err)
		os.Exit(1)
	}
}

// newProxy returns an http.Handler that forwards every request it
// receives to the Unix socket at socketPath, streaming the response
// back with a single addition: Access-Control-Allow-Origin, so a
// browser page on a different origin (the Playwright harness's static
// file server for gui.html) can fetch() it. Chromium enforces CORS
// against 127.0.0.1 the same as any other origin, and this proxy's
// only consumer is that browser-driven test suite, so allowing any
// origin here doesn't widen a real attack surface.
func newProxy(socketPath string) http.Handler {
	reverseProxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = "unix"
			pr.Out.URL.Path = pr.In.URL.Path
			pr.Out.URL.RawQuery = pr.In.URL.RawQuery
			pr.Out.Host = "localhost"
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
			},
		},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		reverseProxy.ServeHTTP(w, r)
	})
}

// run listens on 127.0.0.1:port and forwards every request to the
// Unix socket at socketPath until the process receives SIGINT/SIGTERM.
func run(port int, socketPath string) error {
	srv := &http.Server{
		Addr:              "127.0.0.1:" + strconv.Itoa(port),
		Handler:           newProxy(socketPath),
		ReadHeaderTimeout: 5 * time.Second,
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	err := srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
