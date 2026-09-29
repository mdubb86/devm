package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

const proposeEndpoint = "http://192.168.127.1:82/propose"

// proposeBody is the wire shape both `gdevm propose` and `devm propose`
// send. It intentionally names no file: the daemon scans every
// proposable file (devm.yaml, devm.me.yaml, devm.sh, devm.me.sh)
// against its last-approved snapshot and includes every one that
// diverged. Reporting a single file was error-prone — the operator
// had to guess which file they'd edited most recently and often got
// "no changes since last approval" when their real edit was in a
// different file.
type proposeBody struct {
	Cwd    string `json:"cwd"`
	Branch string `json:"branch"`
	Reason string `json:"reason"`
	Source string `json:"source"`
}

// proposeMain implements `gdevm propose`. Returns the process exit
// code. The binary sends only signal-and-attribution metadata — the
// edited config's bytes reach the Mac via the mutagen sync session.
//
// Reaches the daemon over softnet: guest TCP 192.168.127.1:82 is
// forwarded (softnet ForwardTargets.Propose, per project) to the
// daemon's per-project propose HTTP listener.
func proposeMain(args []string) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm propose: cwd: %v\n", err)
		return 1
	}
	return runPropose(args, proposeEndpoint, cwd)
}

const proposeUsage = `usage: gdevm propose [--reason "<why>"]

Signal to the Mac side that one or more project config files
(devm.yaml, devm.me.yaml, devm.sh, devm.me.sh) were edited from
inside the guest. The daemon scans every proposable file against
its last-approved snapshot and records every one that diverged —
you never have to name which file you changed.

Flags:
  --reason "<why>"    Short description of the change for the reviewer.
                      Optional but strongly encouraged.
  -h, --help          Print this help.
`

func runPropose(args []string, endpoint, cwd string) int {
	reason := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Print(proposeUsage)
			return 0
		case "--reason":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "gdevm propose: --reason requires a value")
				return 2
			}
			reason = args[i+1]
			i++
		default:
			fmt.Fprintf(os.Stderr, "gdevm propose: unknown arg %q\n%s", args[i], proposeUsage)
			return 2
		}
	}
	return doPost(endpoint, cwd, gitBranch(cwd), reason)
}

func doPost(endpoint, cwd, branch, reason string) int {
	body, _ := json.Marshal(proposeBody{
		Cwd:    cwd,
		Branch: branch,
		Reason: reason,
		Source: "guest",
	})
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm propose: %v\n", err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm propose: cannot reach devm daemon on 192.168.127.1:82 — is the VM properly started?\n%v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return 0
	}
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		// Daemon short-circuited (e.g. no changes since last approval);
		// stream the human-readable body straight to stdout and exit 0.
		fmt.Print(string(respBody))
		return 0
	case http.StatusForbidden:
		fmt.Fprintf(os.Stderr, "gdevm propose: %s\n", strings.TrimSpace(string(respBody)))
		return 3
	case http.StatusNotFound:
		fmt.Fprintln(os.Stderr, "gdevm propose: daemon does not support propose channel — upgrade the Mac side")
		return 2
	case http.StatusBadRequest:
		fmt.Fprintf(os.Stderr, "gdevm propose: %s\n", strings.TrimSpace(string(respBody)))
		return 2
	default:
		fmt.Fprintf(os.Stderr, "gdevm propose: daemon returned %d: %s\n", resp.StatusCode, strings.TrimSpace(string(respBody)))
		return 1
	}
}

func gitBranch(cwd string) string {
	out, err := exec.Command("git", "-C", cwd, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
