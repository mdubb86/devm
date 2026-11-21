package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
)

// logsMain implements `gdevm logs [-f|--follow] <name>`. GETs
// /v1/logs?name=<name>&follow=<bool> and copies the response body
// to stdout. Returns 0 on EOF (run complete, log fully consumed),
// 1 if the daemon is unreachable or the run is unknown.
func logsMain(args []string) int {
	fs := flag.NewFlagSet("gdevm logs", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	follow := fs.Bool("follow", false, "tail the log as the run writes it")
	fs.BoolVar(follow, "f", false, "shorthand for --follow")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gdevm logs [-f|--follow] <name>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 || rest[0] == "" {
		fs.Usage()
		return 2
	}
	name := rest[0]

	u := clientURL("/v1/logs") + "?name=" + url.QueryEscape(name)
	if *follow {
		u += "&follow=true"
	}

	// Cancel the request (and stop reading) on Ctrl-C so a
	// long-running `gdevm logs -f` exits cleanly from an operator's
	// terminal.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm logs: build request: %v\n", err)
		return 1
	}
	resp, err := clientDo(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm logs: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(os.Stderr, resp.Body)
		return 1
	}
	_, err = io.Copy(os.Stdout, resp.Body)
	if err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "gdevm logs: %v\n", err)
		return 1
	}
	return 0
}
