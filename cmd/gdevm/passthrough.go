package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const passthroughEndpoint = "http://192.168.127.1:82/passthrough"

type passthroughRequestBody struct {
	Reason          string `json:"reason"`
	DurationSeconds int    `json:"duration_seconds,omitempty"`
	Cwd             string `json:"cwd,omitempty"`
	Branch          string `json:"branch,omitempty"`
	Source          string `json:"source"`
}

// passthroughMain implements `gdevm passthrough`. Submits a request
// for an egress passthrough window awaiting Mac-side approval. Does
// NOT open the window itself — a human runs `devm passthrough
// approve` on the Mac side to authorize it.
//
// Reaches the daemon over softnet: guest TCP 192.168.127.1:82 is
// forwarded (softnet ForwardTargets.Propose, per project) to the
// daemon's per-project gdevm-API listener (shared with propose).
func passthroughMain(args []string) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm passthrough: cwd: %v\n", err)
		return 1
	}
	return runPassthrough(args, passthroughEndpoint, cwd)
}

func runPassthrough(args []string, endpoint, cwd string) int {
	reason := ""
	forArg := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--reason":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "gdevm passthrough: --reason requires a value")
				return 2
			}
			reason = args[i+1]
			i++
		case "--for":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "gdevm passthrough: --for requires a value (e.g. 5m, 24h)")
				return 2
			}
			forArg = args[i+1]
			i++
		default:
			fmt.Fprintf(os.Stderr, "gdevm passthrough: unknown arg %q\n", args[i])
			return 2
		}
	}
	if reason == "" {
		fmt.Fprintln(os.Stderr, "gdevm passthrough: --reason is required (short justification the Mac reviewer sees)")
		return 2
	}
	durationSeconds := 0
	if forArg != "" {
		d, err := time.ParseDuration(forArg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gdevm passthrough: --for: %v\n", err)
			return 2
		}
		if d < time.Second {
			fmt.Fprintf(os.Stderr, "gdevm passthrough: --for must be at least 1s (got %s)\n", d)
			return 2
		}
		durationSeconds = int(d.Round(time.Second) / time.Second)
	}
	return doPassthroughPost(endpoint, cwd, gitBranch(cwd), reason, durationSeconds)
}

func doPassthroughPost(endpoint, cwd, branch, reason string, durationSeconds int) int {
	body, _ := json.Marshal(passthroughRequestBody{
		Reason:          reason,
		DurationSeconds: durationSeconds,
		Cwd:             cwd,
		Branch:          branch,
		Source:          "guest",
	})
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm passthrough: %v\n", err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm passthrough: cannot reach devm daemon on 192.168.127.1:82 — is the VM properly started?\n%v\n", err)
		return 1
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusAccepted:
		// Daemon queued the request; echo its human-readable body.
		fmt.Print(string(respBody))
		return 0
	case http.StatusForbidden:
		fmt.Fprintf(os.Stderr, "gdevm passthrough: %s\n", strings.TrimSpace(string(respBody)))
		return 3
	case http.StatusNotFound:
		fmt.Fprintln(os.Stderr, "gdevm passthrough: daemon does not support passthrough requests — upgrade the Mac side")
		return 2
	case http.StatusBadRequest, http.StatusPreconditionFailed:
		fmt.Fprintf(os.Stderr, "gdevm passthrough: %s\n", strings.TrimSpace(string(respBody)))
		return 2
	default:
		fmt.Fprintf(os.Stderr, "gdevm passthrough: daemon returned %d: %s\n", resp.StatusCode, strings.TrimSpace(string(respBody)))
		return 1
	}
}
