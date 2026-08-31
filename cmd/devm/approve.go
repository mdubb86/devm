package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/repohelpers"
)

type approveOpts struct {
	daemonURL string
	projectID string
	macCwd    string
	stdin     io.Reader
	stdout    io.Writer
	stderr    io.Writer
}

var approveCmd = &cobra.Command{
	Use:   "approve",
	Short: "Review and approve pending devm.yaml changes",
	Long: `Compares the project's devm.yaml (and devm.me.yaml) against the
last-approved snapshot, prints a colored unified diff of the differences,
and prompts to approve. No changes are applied by this command; approving
only advances the daemon's "what the user OK'd" snapshot so that
subsequent ` + "`devm reconcile`" + ` / ` + "`devm start`" + ` proceed.

This command NEVER accepts a --yes flag: the human must be present at
the terminal to answer. Scripts cannot approve.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve cwd: %w", err)
		}
		pid, err := resolveProjectID(cwd)
		if err != nil {
			return err
		}
		return runApprove(approveOpts{
			daemonURL: daemonURL(),
			projectID: pid,
			macCwd:    cwd,
			stdin:     os.Stdin,
			stdout:    os.Stdout,
			stderr:    os.Stderr,
		})
	},
}

func runApprove(o approveOpts) error {
	u, err := url.Parse(o.daemonURL + "/vm/approve-state")
	if err != nil {
		return fmt.Errorf("build approve-state URL: %w", err)
	}
	q := u.Query()
	q.Set("project", o.projectID)
	q.Set("mac_cwd", o.macCwd)
	u.RawQuery = q.Encode()
	resp, err := http.Get(u.String())
	if err != nil {
		return fmt.Errorf("GET %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errors.New("daemon does not support approve gate — reinstall daemon with `devm install`")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("approve-state returned %s: %s", resp.Status, body)
	}
	var state struct {
		Diverged          bool    `json:"diverged"`
		CurrentDevmBytes  string  `json:"current_devm_bytes"`
		ApprovedDevmBytes *string `json:"approved_devm_bytes"`
		CurrentMeBytes    *string `json:"current_me_bytes"`
		ApprovedMeBytes   *string `json:"approved_me_bytes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return fmt.Errorf("decode approve-state: %w", err)
	}
	if !state.Diverged {
		fmt.Fprintln(o.stdout, "devm.yaml is already approved (no changes).")
		return nil
	}
	printDiffSection(o.stdout, "devm.yaml", decodeOrNil(state.ApprovedDevmBytes), decodeOrNil(&state.CurrentDevmBytes))
	printDiffSection(o.stdout, "devm.me.yaml", decodeOrNil(state.ApprovedMeBytes), decodeOrNil(state.CurrentMeBytes))
	fmt.Fprint(o.stdout, "\nApprove these changes? [y/N] ")
	reader := bufio.NewReader(o.stdin)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read answer: %w", err)
	}
	ans := strings.TrimSpace(strings.ToLower(line))
	if ans != "y" && ans != "yes" {
		return errors.New("not approved")
	}
	u2, _ := url.Parse(o.daemonURL + "/vm/approve")
	q2 := u2.Query()
	q2.Set("project", o.projectID)
	q2.Set("mac_cwd", o.macCwd)
	u2.RawQuery = q2.Encode()
	rsp, err := http.Post(u2.String(), "application/json", nil)
	if err != nil {
		return fmt.Errorf("POST %s: %w", u2, err)
	}
	defer rsp.Body.Close()
	if rsp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(rsp.Body)
		return fmt.Errorf("approve returned %s: %s", rsp.Status, body)
	}
	fmt.Fprintln(o.stdout, "approved.")
	return nil
}

func decodeOrNil(b64 *string) []byte {
	if b64 == nil || *b64 == "" {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(*b64)
	if err != nil {
		return nil
	}
	return raw
}

// printDiffSection emits a colored unified diff for one file. Uses
// the standard "+"/"−"/space prefix + ANSI color codes; deliberately
// unified format so the CLI diff mirrors what the daemon has, not
// what YAML would canonicalize to.
func printDiffSection(w io.Writer, name string, old, new []byte) {
	if old == nil && new == nil {
		return
	}
	fmt.Fprintf(w, "\n--- %s (approved)\n+++ %s (current)\n", name, name)
	oldLines := splitLines(old)
	newLines := splitLines(new)
	// Simple line-by-line diff — for tiny files this is fine. If it
	// gets noisy in practice, upgrade to Myers via
	// github.com/pmezard/go-difflib in a follow-up.
	i, j := 0, 0
	for i < len(oldLines) || j < len(newLines) {
		switch {
		case i < len(oldLines) && j < len(newLines) && oldLines[i] == newLines[j]:
			fmt.Fprintf(w, " %s\n", oldLines[i])
			i++
			j++
		case i < len(oldLines) && (j >= len(newLines) || oldLines[i] != newLines[j]):
			fmt.Fprintf(w, "\x1b[31m-%s\x1b[0m\n", oldLines[i])
			i++
		default:
			fmt.Fprintf(w, "\x1b[32m+%s\x1b[0m\n", newLines[j])
			j++
		}
	}
}

func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	s := string(b)
	if strings.HasSuffix(s, "\n") {
		s = s[:len(s)-1]
	}
	return strings.Split(s, "\n")
}

// daemonURL returns the base URL for reaching the daemon over HTTP.
// The daemon listens on a Unix socket but uses HTTP request semantics,
// so this returns the localhost URL used by serviceapi.Client.
func daemonURL() string {
	return "http://localhost"
}

// resolveProjectID loads the devm.yaml from cwd and returns the project.name.
func resolveProjectID(cwd string) (string, error) {
	repoRoot, err := repohelpers.FindDevmYAML(cwd)
	if err != nil {
		return "", err
	}
	cfg, err := config.Load(repoRoot)
	if err != nil {
		return "", fmt.Errorf("locate devm.yaml: %w", err)
	}
	return cfg.Project.Name, nil
}

func init() {
	rootCmd.AddCommand(approveCmd)
}
