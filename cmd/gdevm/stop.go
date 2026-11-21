package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
)

// stopMain implements `gdevm stop <name>`. POSTs /v1/stop?name=<name>
// to the gdevm serve daemon, which SIGTERMs the run's process group
// with a short grace window before SIGKILL. Returns 0 on success, 1
// if the run is unknown or the daemon is unreachable.
func stopMain(args []string) int {
	if len(args) == 0 || args[0] == "" {
		fmt.Fprintln(os.Stderr, "usage: gdevm stop <name>")
		return 2
	}
	name := args[0]

	u := clientURL("/v1/stop") + "?name=" + url.QueryEscape(name)
	ctx, cancel := context.WithTimeout(context.Background(), shortClientTimeout+gracefulStopTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm stop: build request: %v\n", err)
		return 1
	}
	resp, err := clientDo(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm stop: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return 0
	}
	// Non-204: forward the server's error body to stderr.
	_, _ = io.Copy(os.Stderr, resp.Body)
	return 1
}
