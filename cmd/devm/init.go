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
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init [name]",
	Short: "Register the current directory as a devm project.",
	Long: `Creates a per-project state directory (holding devm.yaml + supporting
state), writes a seed devm.yaml, and registers the current working
directory so future devm commands from here resolve to this project.

When name is omitted, it defaults to the basename of the current
directory. Passing an explicit name overrides that default — useful
when the cwd basename collides with a devm-internal storage dir
(bin, state, iron-proxy, mutagen, mutagen-ssh-dir, ssh, secrets,
ca, softnet-bin, volumes) or when you want a rename.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("init: getcwd: %w", err)
		}
		name := resolveInitName(args, cwd)
		socketPath := cfg.SocketPath()
		httpc := &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
				},
			},
		}
		out, err := runInitWithClient("http://localhost", cwd, name, httpc)
		if err != nil {
			return err
		}
		fmt.Println(out)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
}

// resolveInitName picks the project name for `devm init`. An explicit
// positional arg wins; otherwise fall back to the cwd basename — the
// convention users had under the pre-branch model where project.name
// was hand-written into devm.yaml and almost always matched the dir.
func resolveInitName(args []string, cwd string) string {
	if len(args) == 1 {
		return args[0]
	}
	return filepath.Base(cwd)
}

// registerProjectResponse mirrors internal/serviceapi's response body
// for POST /vm/register-project.
type registerProjectResponse struct {
	Name       string `json:"name"`
	ConfigPath string `json:"config_path"`
}

// runInit is the testable seam: real callers use initCmd, which wires
// in the daemon's Unix-socket client; tests hit an httptest.Server
// with the default client.
func runInit(baseURL, cwd, name string) (string, error) {
	return runInitWithClient(baseURL, cwd, name, http.DefaultClient)
}

func runInitWithClient(baseURL, cwd, name string, httpClient *http.Client) (string, error) {
	u := baseURL + "/vm/register-project?name=" + url.QueryEscape(name) + "&cwd=" + url.QueryEscape(cwd)
	resp, err := httpClient.Post(u, "application/json", bytes.NewReader(nil))
	if err != nil {
		return "", fmt.Errorf("init: reach daemon: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("init: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s", strings.TrimSpace(string(body)))
	}
	var out registerProjectResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("init: decode: %w", err)
	}
	return fmt.Sprintf("Registered project %q for %s.\nEdit %s to configure your sandbox.", out.Name, cwd, out.ConfigPath), nil
}
