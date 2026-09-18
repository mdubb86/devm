// propose is the guest-side binary that signals a devm.yaml edit to
// the Mac side. The binary itself sends no YAML body; mutagen sync
// delivers the bytes separately.
//
// Usage:
//
//	propose [--reason <text>] [--kind devm.yaml|devm.me.yaml]
//
// Reaches the daemon over softnet: guest TCP 192.168.127.1:82 is
// forwarded (softnet ForwardTargets.Propose, per project) to the
// daemon's per-project propose HTTP listener.
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

type proposeBody struct {
	Cwd    string `json:"cwd"`
	Branch string `json:"branch"`
	Reason string `json:"reason"`
	Kind   string `json:"kind"`
	Source string `json:"source"`
}

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "propose: cwd: %v\n", err)
		os.Exit(1)
	}
	os.Exit(run(os.Args[1:], proposeEndpoint, cwd))
}

func run(args []string, endpoint, cwd string) int {
	reason := ""
	kind := "devm.yaml"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--reason":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "propose: --reason requires a value")
				return 2
			}
			reason = args[i+1]
			i++
		case "--kind":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "propose: --kind requires a value")
				return 2
			}
			kind = args[i+1]
			i++
		default:
			fmt.Fprintf(os.Stderr, "propose: unknown arg %q\n", args[i])
			return 2
		}
	}
	return doPost(endpoint, cwd, gitBranch(cwd), reason, kind)
}

func doPost(endpoint, cwd, branch, reason, kind string) int {
	body, _ := json.Marshal(proposeBody{
		Cwd:    cwd,
		Branch: branch,
		Reason: reason,
		Kind:   kind,
		Source: "guest",
	})
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "propose: %v\n", err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "propose: cannot reach devm daemon on 192.168.127.1:82 — is the VM properly started?\n%v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return 0
	}
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusNotFound:
		fmt.Fprintln(os.Stderr, "propose: daemon does not support propose channel — upgrade the Mac side")
		return 2
	case http.StatusBadRequest:
		fmt.Fprintf(os.Stderr, "propose: %s\n", strings.TrimSpace(string(respBody)))
		return 2
	default:
		fmt.Fprintf(os.Stderr, "propose: daemon returned %d: %s\n", resp.StatusCode, strings.TrimSpace(string(respBody)))
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
