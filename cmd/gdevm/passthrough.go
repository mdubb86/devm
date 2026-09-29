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

const passthroughUsage = `usage: gdevm passthrough <duration> --reason "<why>"

Request a Mac-approved egress passthrough window of the given duration.
The Mac operator sees the reason and decides whether to open the window
via ` + "`devm passthrough approve`" + `.

Arguments:
  <duration>          Go duration string, e.g. 30s, 5m, 24h. Required —
                      there is no default. The Mac side opens the window
                      for exactly this long.

Flags:
  --reason "<why>"    Short justification the Mac reviewer sees. Required.
  -h, --help          Print this help.
`

func runPassthrough(args []string, endpoint, cwd string) int {
	reason := ""
	durationArg := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Print(passthroughUsage)
			return 0
		case "--reason":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "gdevm passthrough: --reason requires a value")
				return 2
			}
			reason = args[i+1]
			i++
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(os.Stderr, "gdevm passthrough: unknown flag %q\n%s", args[i], passthroughUsage)
				return 2
			}
			if durationArg != "" {
				fmt.Fprintf(os.Stderr, "gdevm passthrough: unexpected extra arg %q (duration already set to %q)\n%s", args[i], durationArg, passthroughUsage)
				return 2
			}
			durationArg = args[i]
		}
	}
	if durationArg == "" {
		fmt.Fprintln(os.Stderr, "gdevm passthrough: duration is required (e.g. 5m, 24h) — pass it as the first positional argument")
		fmt.Fprint(os.Stderr, passthroughUsage)
		return 2
	}
	if reason == "" {
		fmt.Fprintln(os.Stderr, "gdevm passthrough: --reason is required (short justification the Mac reviewer sees)")
		return 2
	}
	d, err := time.ParseDuration(durationArg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm passthrough: duration: %v (e.g. 30s, 5m, 24h)\n", err)
		return 2
	}
	if d < time.Second {
		fmt.Fprintf(os.Stderr, "gdevm passthrough: duration must be at least 1s (got %s)\n", d)
		return 2
	}
	durationSeconds := int(d.Round(time.Second) / time.Second)
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
