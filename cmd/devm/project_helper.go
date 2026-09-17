package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// ResolvedProject is what resolveProjectFromCwd returns on match:
// the daemon's canonical project name and the state directory (under
// which devm.yaml, devm.me.yaml, approved-snapshot/ etc. live).
type ResolvedProject struct {
	Name     string `json:"name"`
	StateDir string `json:"state_dir"`
}

// resolveProjectFn is resolveProjectFromCwd by default; tests override
// it to inject a fake resolver and bypass the daemon socket.
var resolveProjectFn = resolveProjectFromCwd

// resolveProjectFromCwd POSTs os.Getwd() to /vm/resolve-project and
// returns the daemon's answer. On 404, returns an error whose message
// is the daemon's response body (containing the `devm init` hint).
func resolveProjectFromCwd() (ResolvedProject, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return ResolvedProject{}, fmt.Errorf("resolve-project: getcwd: %w", err)
	}
	socketPath := cfg.SocketPath()
	httpc := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
			},
		},
	}
	return resolveProjectFromURLWithClient("http://localhost", cwd, httpc)
}

// resolveProjectFromURL is the testable form. Real callers use
// resolveProjectFromCwd, which fills in the daemon's Unix-socket URL.
func resolveProjectFromURL(baseURL, cwd string) (ResolvedProject, error) {
	return resolveProjectFromURLWithClient(baseURL, cwd, http.DefaultClient)
}

// resolveProjectFromURLWithClient is the internal form that allows
// dependency injection of an http.Client with a custom Transport.
func resolveProjectFromURLWithClient(baseURL, cwd string, httpClient *http.Client) (ResolvedProject, error) {
	u := baseURL + "/vm/resolve-project?cwd=" + url.QueryEscape(cwd)
	resp, err := httpClient.Post(u, "application/json", bytes.NewReader(nil))
	if err != nil {
		return ResolvedProject{}, fmt.Errorf("resolve-project: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return ResolvedProject{}, fmt.Errorf("%s", strings.TrimSpace(string(body)))
	}
	var out ResolvedProject
	if err := json.Unmarshal(body, &out); err != nil {
		return ResolvedProject{}, fmt.Errorf("resolve-project: decode: %w", err)
	}
	return out, nil
}
