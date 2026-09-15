// propose is the guest-side binary that ships a devm.yaml edit to
// the Mac side. Installed at /opt/devm/bin/propose via the
// provisioning bundle.
//
// Usage:
//
//	propose [--reason <text>] [<path>]
//
// With no path, reads YAML from stdin. With a path, reads that file.
// Reaches the daemon over softnet: guest TCP 192.168.127.1:82 is
// forwarded (softnet ForwardTargets.Propose, per project) to the
// daemon's per-project propose HTTP listener.
package main

import (
	"bytes"
	"encoding/base64"
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

// proposeBody mirrors internal/serviceapi/propose.go's proposeRequest.
// Duplicated by design — cmd/propose is a small guest binary with no
// dependency on internal packages.
type proposeBody struct {
	YAML   string `json:"yaml"`
	Cwd    string `json:"cwd"`
	Branch string `json:"branch"`
	Reason string `json:"reason"`
	Kind   string `json:"kind"`
}

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "propose: cannot resolve cwd: %v\n", err)
		os.Exit(1)
	}
	os.Exit(run(os.Args[1:], os.Stdin, proposeEndpoint, cwd))
}

// run is the testable entry point. args are the CLI arguments after
// argv[0]; stdin supplies YAML when no path arg is given; endpoint
// is the propose URL; cwd is used for attribution + git branch
// discovery.
func run(args []string, stdin io.Reader, endpoint, cwd string) int {
	reason := ""
	var pathArg string
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--reason":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "propose: --reason requires a value")
				return 2
			}
			reason = args[i+1]
			i += 2
		default:
			pathArg = args[i]
			i++
		}
	}

	var yamlBytes []byte
	if pathArg == "" {
		body, err := io.ReadAll(stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "propose: read stdin: %v\n", err)
			return 1
		}
		yamlBytes = body
	} else {
		body, err := os.ReadFile(pathArg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "propose: read %s: %v\n", pathArg, err)
			return 1
		}
		yamlBytes = body
	}

	branch := gitBranch(cwd)
	return doPost(endpoint, yamlBytes, cwd, branch, reason)
}

// doPost is split from run so tests can drive it directly.
func doPost(endpoint string, yamlBytes []byte, cwd, branch, reason string) int {
	body, err := json.Marshal(proposeBody{
		YAML:   base64.StdEncoding.EncodeToString(yamlBytes),
		Cwd:    cwd,
		Branch: branch,
		Reason: reason,
		Kind:   "devm.yaml",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "propose: marshal: %v\n", err)
		return 1
	}
	httpReq, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "propose: build request: %v\n", err)
		return 1
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		fmt.Fprintf(os.Stderr, "propose: could not reach devm daemon on 192.168.127.1:82 — is the VM properly started?\n%v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return 0
	}
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusBadRequest {
		fmt.Fprintf(os.Stderr, "propose: %s\n", strings.TrimSpace(string(respBody)))
		return 2
	}
	if resp.StatusCode == http.StatusNotFound {
		fmt.Fprintln(os.Stderr, "propose: daemon does not support propose channel — upgrade the Mac side")
		return 2
	}
	fmt.Fprintf(os.Stderr, "propose: daemon returned %d: %s\n", resp.StatusCode, strings.TrimSpace(string(respBody)))
	return 1
}

// gitBranch returns the current branch name for cwd, or empty
// string if cwd isn't inside a git repo (or git isn't installed).
// Never a hard error.
func gitBranch(cwd string) string {
	cmd := exec.Command("git", "-C", cwd, "symbolic-ref", "--short", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
