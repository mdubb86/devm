package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/secret"

	"github.com/spf13/cobra"
)

var secretCmd = &cobra.Command{
	Use:   "secret",
	Short: "Manage per-project secrets in the macOS login keychain",
}

var secretSetCmd = &cobra.Command{
	Use:   "set <name>",
	Short: "Set a secret (reads value from stdin if piped, else TTY prompt)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		projectID, err := currentProjectID()
		if err != nil {
			return err
		}
		return runSecretSet(secret.NewMacKeychain(), projectID, args[0], os.Stdin)
	},
}

func runSecretSet(b secret.Backend, projectID, name string, stdin io.Reader) error {
	var value string
	if isTerminal(stdin) {
		fmt.Fprintf(os.Stderr, "Enter value for %s/%s: ", projectID, name)
		v, err := readSecretNoEcho()
		if err != nil {
			return fmt.Errorf("read value: %w", err)
		}
		value = v
		fmt.Fprintln(os.Stderr)
	} else {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
		value = strings.TrimRight(string(data), "\r\n")
	}
	if value == "" {
		return errors.New("empty value rejected")
	}
	return b.Set(projectID+"/"+name, value)
}

var secretGetReveal bool
var secretGetCmd = &cobra.Command{
	Use:   "get <name>",
	Short: "Print a secret (masked unless --reveal)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		projectID, err := currentProjectID()
		if err != nil {
			return err
		}
		return runSecretGet(secret.NewMacKeychain(), projectID, args[0], secretGetReveal, os.Stdout)
	},
}

func runSecretGet(b secret.Backend, projectID, name string, reveal bool, out io.Writer) error {
	v, err := b.Get(projectID + "/" + name)
	if err != nil {
		return err
	}
	if reveal {
		fmt.Fprintln(out, v)
		return nil
	}
	fmt.Fprintln(out, mask(v))
	return nil
}

func mask(s string) string {
	if len(s) <= 4 {
		return strings.Repeat("*", len(s))
	}
	return s[:2] + strings.Repeat("*", len(s)-4) + s[len(s)-2:]
}

var secretListCmd = &cobra.Command{
	Use:   "list",
	Short: "List secrets for the current project",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		projectID, err := currentProjectID()
		if err != nil {
			return err
		}
		return runSecretList(secret.NewMacKeychain(), projectID, os.Stdout)
	},
}

func runSecretList(b secret.Backend, projectID string, out io.Writer) error {
	names, err := b.List(projectID)
	if err != nil {
		return err
	}
	for _, n := range names {
		fmt.Fprintln(out, n)
	}
	return nil
}

var secretDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a secret from the current project",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		projectID, err := currentProjectID()
		if err != nil {
			return err
		}
		return runSecretDelete(secret.NewMacKeychain(), projectID, args[0])
	},
}

func runSecretDelete(b secret.Backend, projectID, name string) error {
	return b.Delete(projectID + "/" + name)
}

func currentProjectID() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		return "", fmt.Errorf("locate devm.yaml: %w (run `devm secret` from a project root)", err)
	}
	return cfg.Project.Name, nil
}

func init() {
	secretGetCmd.Flags().BoolVar(&secretGetReveal, "reveal", false, "Print the raw value instead of masking")
	secretCmd.AddCommand(secretSetCmd, secretGetCmd, secretListCmd, secretDeleteCmd)
	rootCmd.AddCommand(secretCmd)
}
