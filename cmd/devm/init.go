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

	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init <name>",
	Short: "Register the current directory as a devm project.",
	Long: `Creates a per-project state directory (holding devm.yaml + supporting
state), writes a seed devm.yaml, and registers the current working
directory so future devm commands from here resolve to this project.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("init: getcwd: %w", err)
		}
		socketPath := cfg.SocketPath()
		httpc := &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
				},
			},
		}
		out, err := runInitWithClient("http://localhost", cwd, args[0], httpc)
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
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s", strings.TrimSpace(string(body)))
	}
	var out registerProjectResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("init: decode: %w", err)
	}
	return fmt.Sprintf("Registered project %q for %s.\nEdit %s to configure your sandbox.", out.Name, cwd, out.ConfigPath), nil
}
