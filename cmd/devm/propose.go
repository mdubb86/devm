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
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

var proposeCmd = &cobra.Command{
	Use:   "propose",
	Short: "Signal the daemon that devm.yaml (or devm.me.yaml) has been edited and is ready for review.",
	RunE: func(cmd *cobra.Command, args []string) error {
		reason, _ := cmd.Flags().GetString("reason")
		kind, _ := cmd.Flags().GetString("kind")
		socketPath := cfg.SocketPath()
		httpc := &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
				},
			},
		}
		code := runMacProposeWithClient("http://localhost", reason, kind, httpc)
		if code != 0 {
			os.Exit(code)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(proposeCmd)
	proposeCmd.Flags().String("reason", "", "Optional human-readable reason for the change.")
	proposeCmd.Flags().String("kind", "devm.yaml", "Which config file this proposal targets: devm.yaml, devm.me.yaml, devm.sh, or devm.me.sh.")
}

// runMacPropose is the testable seam: uses http.DefaultClient for tests.
// Real CLI callers use runMacProposeWithClient with a Unix-socket client.
func runMacPropose(baseURL, reason, kind string) int {
	return runMacProposeWithClient(baseURL, reason, kind, http.DefaultClient)
}

// runMacProposeWithClient does the actual work with an injectable http.Client.
// Returns: 0 on 204 success; 2 on a local discovery failure or a daemon 4xx
// response (reached the daemon, it rejected the request); 1 on a transport
// error (couldn't reach the daemon at all, e.g. the socket is down) or a
// daemon 5xx response.
func runMacProposeWithClient(baseURL, reason, kind string, client *http.Client) int {
	rp, err := discoverProjectFn()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 2
	}

	body, _ := json.Marshal(map[string]string{
		"cwd":    rp.MacCwd,
		"branch": gitBranchMac(rp.MacCwd),
		"reason": reason,
		"kind":   kind,
		"source": "mac",
	})

	u := baseURL + "/vm/propose?project=" + url.QueryEscape(rp.Name)
	req, _ := http.NewRequest(http.MethodPost, u, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "propose: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		fmt.Fprintf(os.Stdout, "Proposal recorded for %q. Approve via 'devm approve' or the menu bar.\n", rp.Name)
		return 0
	}

	msg, _ := io.ReadAll(resp.Body)
	fmt.Fprintf(os.Stderr, "propose: %s\n", strings.TrimSpace(string(msg)))

	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return 2
	}
	return 1
}

// gitBranchMac gets the current branch name via git symbolic-ref.
// Returns empty string if git fails (detached HEAD, no git, etc).
func gitBranchMac(cwd string) string {
	out, err := exec.Command("git", "-C", cwd, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
