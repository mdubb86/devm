package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/spf13/cobra"
)

var resolveOpen bool

var resolveCmd = &cobra.Command{
	Use:   "resolve <path>",
	Short: "Translate a VM-emitted path to its Mac-side volume storage location",
	Long: `A path a guest process printed (a $WORKSPACE-anchored path that
literally lives in the VM's clone) isn't reachable on the Mac side —
the clone lives in a volume's Mac-side storage dir instead. devm
resolve translates it: absolute paths are matched by workspace
prefix, relative paths by matching the current directory against a
known workspace.

With --open, the resolved path is opened via ` + "`open`" + ` instead of printed.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		registry, err := serviceapi.NewClient(cfg).Workspaces(cmd.Context())
		if err != nil {
			return fmt.Errorf("query daemon: %w", err)
		}
		resolved, err := resolvePath(args[0], cwd, registry)
		if err != nil {
			return err
		}
		if resolveOpen {
			return exec.Command("open", resolved).Run()
		}
		fmt.Println(resolved)
		return nil
	},
}

// resolvePath translates input against registry. Absolute inputs are
// matched by prefix against each workspace's GuestPath. Relative
// inputs are resolved against cwd instead — cwd must itself sit
// inside a known workspace's GuestPath — since a relative path alone
// carries no information about which workspace it's anchored in.
func resolvePath(input, cwd string, registry []serviceapi.WorkspaceEntry) (string, error) {
	if filepath.IsAbs(input) {
		for _, w := range registry {
			if input == w.GuestPath || strings.HasPrefix(input, w.GuestPath+string(filepath.Separator)) {
				rel, _ := filepath.Rel(w.GuestPath, input)
				return filepath.Join(w.StoragePath, rel), nil
			}
		}
		return "", fmt.Errorf("path %q is not inside any known devm workspace", input)
	}
	for _, w := range registry {
		if cwd == w.GuestPath || strings.HasPrefix(cwd, w.GuestPath+string(filepath.Separator)) {
			relCwd, _ := filepath.Rel(w.GuestPath, cwd)
			return filepath.Join(w.StoragePath, relCwd, input), nil
		}
	}
	return "", fmt.Errorf("cwd %q is not inside any known devm workspace; cannot resolve relative path %q", cwd, input)
}

func init() {
	resolveCmd.Flags().BoolVar(&resolveOpen, "open", false, "open the resolved path instead of printing it")
	rootCmd.AddCommand(resolveCmd)
}
