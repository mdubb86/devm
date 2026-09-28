package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const refreshBundleEndpoint = "http://192.168.127.1:82/refresh-bundle"

// upgradeMain implements `gdevm upgrade`. POSTs to the daemon's
// per-project /refresh-bundle endpoint (softnet:82 shared listener),
// streams the human-readable summary to stdout, exits 0 on success.
//
// No args; no gate — the refresh ships what the daemon would ship
// anyway.
func upgradeMain(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "gdevm upgrade: no arguments accepted")
		return 2
	}
	return runUpgrade(refreshBundleEndpoint)
}

func runUpgrade(endpoint string) int {
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequest(http.MethodPost, endpoint, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm upgrade: %v\n", err)
		return 1
	}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm upgrade: cannot reach devm daemon on 192.168.127.1:82 — is the VM properly started?\n%v\n", err)
		return 1
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		fmt.Print(string(body))
		return 0
	case http.StatusPreconditionFailed, http.StatusBadRequest:
		fmt.Fprintf(os.Stderr, "gdevm upgrade: %s\n", strings.TrimSpace(string(body)))
		return 2
	case http.StatusNotFound:
		fmt.Fprintln(os.Stderr, "gdevm upgrade: daemon does not support the refresh endpoint — upgrade the Mac side")
		return 2
	default:
		fmt.Fprintf(os.Stderr, "gdevm upgrade: daemon returned %d: %s\n", resp.StatusCode, strings.TrimSpace(string(body)))
		return 1
	}
}
