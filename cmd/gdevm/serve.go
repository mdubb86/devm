package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

const defaultServeAddr = "127.0.0.1:8940"

func serveAddr() string {
	if a := os.Getenv("DEVM_GDEVM_SERVE_ADDR"); a != "" {
		return a
	}
	return defaultServeAddr
}

var (
	servedAddrMu sync.Mutex
	servedAddr   string
)

// setServeAddrForTest records the actual bound address once the
// listener starts. It's harmless in production (any caller could read
// the current bound addr) but only tests care, since
// DEVM_GDEVM_SERVE_ADDR=127.0.0.1:0 picks an ephemeral port whose
// number isn't known until net.Listen returns.
func setServeAddrForTest(addr string) {
	servedAddrMu.Lock()
	defer servedAddrMu.Unlock()
	servedAddr = addr
}

// readServeAddrForTest returns the last address a running serveMain
// bound, or "" if none is currently up.
func readServeAddrForTest() string {
	servedAddrMu.Lock()
	defer servedAddrMu.Unlock()
	return servedAddr
}

// serveMain starts gdevm's guest-side daemon: a long-running process
// bound to a loopback address, serving the v1 HTTP API, until it
// receives SIGTERM/SIGINT. Returns 0 on clean shutdown, 1 on bind
// failure, 2 on bad args.
func serveMain(args []string) int {
	// Reject any args — this subcommand takes none in v1.
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "gdevm serve: takes no arguments")
		return 2
	}

	mux := http.NewServeMux()
	// v1 endpoints registered in a later task; empty mux for now.

	ln, err := net.Listen("tcp", serveAddr())
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm serve: bind %s: %v\n", serveAddr(), err)
		return 1
	}
	setServeAddrForTest(ln.Addr().String())
	defer setServeAddrForTest("")

	srv := &http.Server{Handler: mux}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	<-errCh
	return 0
}
