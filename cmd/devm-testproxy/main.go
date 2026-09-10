// devm-testproxy is a tiny HTTP-over-TCP -> HTTP-over-Unix-socket proxy
// used by the mac/webview Playwright e2e tests. A Chromium extension
// can't dial a Unix socket directly; this binary bridges the gap so
// the extension can reach the real e2e daemon at
// ~/Library/Application Support/devm-e2e/devm.sock over plain TCP.
//
// It is a pure L7 forwarder: no auth, no path rewriting, no filtering.
package main

import (
	"context"
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
// back unmodified.
func newProxy(socketPath string) http.Handler {
	return &httputil.ReverseProxy{
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
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
